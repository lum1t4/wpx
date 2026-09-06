// Package install performs the versioned host installation. The public shell
// bootstrap only downloads and verifies a WPX binary; all material host changes
// live here so they are typed, testable, and tied to a release.
package install

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/platform"
)

const (
	defaultConfigPath = "/etc/wpx/config.json"
	installMarkerPath = "/var/lib/wpx/.installing"
	installMarker     = "WPX installation in progress\n"
)

type installPaths struct {
	ConfigPath      string
	DataRoot        string
	InstalledBinary string
	DependencyRoot  string
	NginxRoot       string
	MySQLRoot       string
	MarkerPath      string
}

type Options struct {
	DryRun              bool
	DisableUpdateChecks bool
	Executable          string
	ConfigPath          string
	Output              io.Writer
}

type Result struct {
	PanelURL       string
	BootstrapToken string
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = defaultConfigPath
	}
	if !filepath.IsAbs(opts.ConfigPath) || filepath.Clean(opts.ConfigPath) != opts.ConfigPath || strings.ContainsAny(opts.ConfigPath, " \t\r\n") {
		return Result{}, errors.New("configuration path must be absolute, clean, and contain no whitespace")
	}
	info, err := platform.Detect()
	if err != nil {
		return Result{}, err
	}
	if err := info.Certified(); err != nil {
		return Result{}, err
	}
	if os.Geteuid() != 0 {
		return Result{}, errors.New("installation must run as root")
	}
	paths := systemInstallPaths(opts.ConfigPath)
	conflicts := detectConflicts(paths)
	resuming := hasInstallMarker(paths.MarkerPath) || isLegacyRcloneFailure(paths)
	if len(conflicts) != 0 && !resuming {
		return Result{}, fmt.Errorf("fresh server required; found: %s", strings.Join(conflicts, ", "))
	}
	steps := []string{
		"install Ubuntu runtime packages", "create the wpx system identity", "install the static WPX binary",
		"create protected state and site directories", "generate the initial panel certificate",
		"write systemd services", "start the broker and web control plane",
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Output, "Certified platform: %s (%s)\n", info.Name, info.Arch)
		for i, step := range steps {
			fmt.Fprintf(opts.Output, "%d. %s\n", i+1, step)
		}
		return Result{}, nil
	}
	// A marker can survive after the panel has started. Load its configuration
	// before making host changes so a retry cannot reset access settings or hide
	// a damaged configuration behind fresh defaults.
	cfg, token, existingConfig, err := installationConfig(opts.ConfigPath, filepath.Join(paths.DataRoot, "state.db"), opts.DisableUpdateChecks)
	if err != nil {
		return Result{}, err
	}
	if err := beginInstall(paths); err != nil {
		return Result{}, err
	}
	runner := commandRunner{output: opts.Output}
	if err := runner.run(ctx, "/usr/bin/apt-get", "update"); err != nil {
		return Result{}, err
	}
	packages := []string{"ca-certificates", "certbot", "curl", "gunicorn", "logrotate", "nginx", "mariadb-server", "redis-server", "nftables", "rsync", "software-properties-common", "unzip", "python3", "python3-certbot-dns-cloudflare", "python3-certbot-dns-route53", "python3-venv"}
	args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
	if err := runner.run(ctx, "/usr/bin/apt-get", args...); err != nil {
		return Result{}, err
	}
	if err := installNginxFastCGICache(); err != nil {
		return Result{}, err
	}
	if err := installLogPolicy(); err != nil {
		return Result{}, err
	}
	// Ubuntu carries one PHP branch. WPX adds the well-established Ondrej PHP
	// PPA so supported branches can coexist, but installs no PHP runtime until a
	// site asks for one.
	if err := runner.run(ctx, "/usr/bin/add-apt-repository", "-y", "ppa:ondrej/php"); err != nil {
		return Result{}, err
	}
	if err := runner.run(ctx, "/usr/bin/apt-get", "update"); err != nil {
		return Result{}, err
	}
	if err := ensureIdentity(ctx, runner); err != nil {
		return Result{}, err
	}
	wpxUser, err := user.Lookup("wpx")
	if err != nil {
		return Result{}, fmt.Errorf("lookup created wpx user: %w", err)
	}
	uid64, _ := strconv.ParseUint(wpxUser.Uid, 10, 32)
	gid64, _ := strconv.ParseUint(wpxUser.Gid, 10, 32)
	uid, gid := int(uid64), int(gid64)
	if err := installExecutable(opts.Executable); err != nil {
		return Result{}, err
	}
	if err := installWPCLI(ctx); err != nil {
		return Result{}, err
	}
	if err := installRestic(ctx); err != nil {
		return Result{}, err
	}
	if err := installRclone(ctx); err != nil {
		return Result{}, err
	}
	for _, dir := range []string{cfg.DataRoot, filepath.Dir(cfg.TLSCertPath), filepath.Dir(cfg.TLSKeyPath), "/var/log/wpx"} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return Result{}, err
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(cfg.SiteRoot, 0750); err != nil {
		return Result{}, err
	}
	// Every site has a private group and Nginx joins only that group. The shared
	// parent therefore grants traversal but not directory listing; otherwise
	// neither a site process nor Nginx can reach the protected per-site tree.
	if err := os.Chmod(cfg.SiteRoot, 0711); err != nil {
		return Result{}, err
	}
	if err := ensurePanelCertificate(cfg.TLSCertPath, cfg.TLSKeyPath, !existingConfig); err != nil {
		return Result{}, err
	}
	if err := os.Chown(cfg.TLSCertPath, uid, gid); err != nil {
		return Result{}, err
	}
	if err := os.Chown(cfg.TLSKeyPath, uid, gid); err != nil {
		return Result{}, err
	}
	if err := ensureSecretKey(cfg.SecretKeyPath, uid, gid, !existingConfig); err != nil {
		return Result{}, err
	}
	cfg.WebUID = uint32(uid)
	if err := config.Save(opts.ConfigPath, cfg); err != nil {
		return Result{}, err
	}
	if err := os.Chown(filepath.Dir(opts.ConfigPath), 0, gid); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(filepath.Dir(opts.ConfigPath), 0750); err != nil {
		return Result{}, err
	}
	if err := os.Chown(opts.ConfigPath, 0, gid); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(opts.ConfigPath, 0640); err != nil {
		return Result{}, err
	}
	if err := WriteUnits(opts.ConfigPath); err != nil {
		return Result{}, err
	}
	if err := runner.run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return Result{}, err
	}
	// Package post-install scripts usually start these services, but WPX makes
	// the desired boot state explicit. WordPress performance defaults must not
	// depend on whether a particular cloud image suppresses service startup.
	if err := runner.run(ctx, "/usr/bin/systemctl", "enable", "--now", "nginx.service", "mariadb.service", "redis-server.service", "certbot.timer"); err != nil {
		return Result{}, err
	}
	if err := runner.run(ctx, "/usr/bin/systemctl", "enable", "wpx-broker.service", "wpx.service"); err != nil {
		return Result{}, err
	}
	// Type=simple reports success before the process has loaded its files. A
	// retry also needs a restart: enable --now leaves an already running process
	// using the old binary and bootstrap token.
	if err := finishInstallation(ctx, cfg, paths.MarkerPath, runner.run, WaitForPanel); err != nil {
		return Result{}, err
	}
	if existingConfig {
		fmt.Fprintln(opts.Output, "Existing access settings are preserved. If an owner already exists, sign in normally; the bootstrap token cannot reopen setup.")
	}
	return Result{PanelURL: installationPanelAddress(cfg, existingConfig), BootstrapToken: token}, nil
}

func installationConfig(path, statePath string, disableUpdateChecks bool) (config.Config, string, bool, error) {
	cfg, err := config.Load(path)
	existing := err == nil
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return config.Config{}, "", false, err
		}
		// WPX saves configuration before its first process can create SQLite.
		// A database (or its sidecars) without config is therefore lost recovery
		// material, not an interrupted first-run bootstrap. New defaults/keys
		// could otherwise make persisted encrypted credentials unrecoverable.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if _, err := os.Lstat(statePath + suffix); err == nil {
				return config.Config{}, "", false, errors.New("configuration is missing but panel state exists; restore the original configuration and secret key before retrying")
			} else if !os.IsNotExist(err) {
				return config.Config{}, "", false, fmt.Errorf("inspect existing panel state: %w", err)
			}
		}
		cfg = config.Default()
		cfg.ListenAddress = "0.0.0.0:9443"
	}
	// Only its hash is retained, so an interrupted run cannot recover the old
	// plaintext setup token. Rotate this one credential; the setup handler stops
	// accepting bootstrap tokens as soon as an owner exists.
	token, hash, err := bootstrapToken()
	if err != nil {
		return config.Config{}, "", false, err
	}
	cfg.BootstrapTokenHash = hash
	if disableUpdateChecks {
		cfg.UpdateChecks = false
	}
	return cfg, token, existing, nil
}

func installationPanelAddress(cfg config.Config, existing bool) string {
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return "existing configured access (settings preserved)"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "https://SERVER_IP:" + port + "/setup"
	}
	address := "https://" + net.JoinHostPort(host, port) + "/setup"
	if ip := net.ParseIP(host); existing && (host == "localhost" || ip != nil && ip.IsLoopback()) {
		// The listener cannot tell us the domain or Tailscale hostname. Do not
		// invent a public URL, or imply that port 9443 was opened by the retry.
		return "your existing domain/Tailscale URL, or " + address + " through an SSH tunnel (access settings preserved)"
	}
	return address
}

func finishInstallation(ctx context.Context, cfg config.Config, markerPath string, run func(context.Context, string, ...string) error, health func(context.Context, config.Config) error) error {
	if err := run(ctx, "/usr/bin/systemctl", "restart", "wpx-broker.service", "wpx.service"); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := health(healthCtx, cfg); err != nil {
		return fmt.Errorf("panel did not become ready; installation can be resumed: %w", err)
	}
	// is-active with several units succeeds when any one is active. Probe
	// separately so a running web process cannot mask a failed dependency.
	for _, service := range []string{"wpx-broker.service", "wpx.service", "nginx.service", "mariadb.service", "redis-server.service"} {
		if err := run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", service); err != nil {
			return fmt.Errorf("required service %s is unavailable; installation can be resumed: %w", service, err)
		}
	}
	if err := os.Remove(markerPath); err != nil {
		return fmt.Errorf("finish resumable installation: %w", err)
	}
	return nil
}

func installNginxFastCGICache() error {
	cacheRoot := "/var/cache/nginx/wpx"
	if err := os.MkdirAll(cacheRoot, 0750); err != nil {
		return fmt.Errorf("create Nginx cache directory: %w", err)
	}
	www, err := user.Lookup("www-data")
	if err != nil {
		return fmt.Errorf("lookup Nginx identity: %w", err)
	}
	uid, err := strconv.Atoi(www.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(www.Gid)
	if err != nil {
		return err
	}
	if err := os.Chown(cacheRoot, uid, gid); err != nil {
		return fmt.Errorf("own Nginx cache directory: %w", err)
	}
	if err := ReconcileNginxCachePermissions(); err != nil {
		return err
	}
	content := []byte("# Managed by WPX. Manual changes will be replaced.\nfastcgi_cache_path /var/cache/nginx/wpx levels=1:2 keys_zone=WPX:100m inactive=60m max_size=2g use_temp_path=off;\nfastcgi_cache_key \"$scheme$request_method$host$request_uri\";\n")
	path := "/etc/nginx/conf.d/wpx-fastcgi-cache.conf"
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wpx-cache-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReconcileNginxCachePermissions keeps the package-owned parent private while
// allowing the configured Nginx worker group to traverse into WPX's cache.
// Ubuntu creates /var/cache/nginx as root:root 0750, which otherwise blocks the
// www-data workers even when the child directory has the right owner.
func ReconcileNginxCachePermissions() error {
	www, err := user.Lookup("www-data")
	if err != nil {
		return fmt.Errorf("lookup Nginx identity: %w", err)
	}
	gid, err := strconv.Atoi(www.Gid)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(www.Uid)
	if err != nil {
		return err
	}
	if err := os.Chown("/var/cache/nginx", 0, gid); err != nil {
		return fmt.Errorf("own Nginx cache parent: %w", err)
	}
	if err := os.Chmod("/var/cache/nginx", 0750); err != nil {
		return fmt.Errorf("protect Nginx cache parent: %w", err)
	}
	if err := os.Chown("/var/cache/nginx/wpx", uid, gid); err != nil {
		return fmt.Errorf("own WPX Nginx cache: %w", err)
	}
	if err := os.Chmod("/var/cache/nginx/wpx", 0750); err != nil {
		return fmt.Errorf("protect WPX Nginx cache: %w", err)
	}
	return nil
}

func ensureSecretKey(path string, uid, gid int, allowCreate bool) error {
	info, err := os.Lstat(path)
	if err == nil {
		// This key encrypts persisted credentials. Replacing it on a retry would
		// leave apparently healthy state whose secrets can never be decrypted.
		if !info.Mode().IsRegular() || info.Size() != 32 {
			return errors.New("existing application secret key is invalid; restore the original 32-byte key")
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("own application secret key: %w", err)
		}
		return os.Chmod(path, 0600)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect application secret key: %w", err)
	}
	if !allowCreate {
		return errors.New("application secret key is missing from an existing installation; restore it before retrying")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate application secret key: %w", err)
	}
	if err := atomicWrite(path, key, 0600); err != nil {
		return fmt.Errorf("write application secret key: %w", err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("protect application secret key: %w", err)
	}
	return nil
}

func systemInstallPaths(configPath string) installPaths {
	return installPaths{
		ConfigPath:      configPath,
		DataRoot:        "/var/lib/wpx",
		InstalledBinary: "/usr/local/bin/wpx",
		DependencyRoot:  "/usr/local/lib/wpx",
		NginxRoot:       "/etc/nginx",
		MySQLRoot:       "/var/lib/mysql",
		MarkerPath:      installMarkerPath,
	}
}

func detectConflicts(paths installPaths) []string {
	candidates := []string{paths.ConfigPath, paths.DataRoot, paths.InstalledBinary, paths.NginxRoot, paths.MySQLRoot}
	var conflicts []string
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			conflicts = append(conflicts, path)
		}
	}
	return conflicts
}

func hasInstallMarker(path string) bool {
	content, err := os.ReadFile(path)
	return err == nil && string(content) == installMarker
}

// Alpha releases before the resumable marker failed immediately after
// installing WP-CLI and restic when rclone's amd64 binary outgrew its bound.
// This exact artifact sequence is strong evidence of that interrupted WPX run;
// accepting anything broader could take ownership of an unrelated web stack.
func isLegacyRcloneFailure(paths installPaths) bool {
	if _, err := os.Stat(paths.ConfigPath); !os.IsNotExist(err) {
		return false
	}
	for _, path := range []string{
		paths.InstalledBinary,
		filepath.Join(paths.DependencyRoot, "wp-cli.phar"),
		filepath.Join(paths.DependencyRoot, "restic"),
	} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	_, err := os.Stat(filepath.Join(paths.DependencyRoot, "rclone"))
	return os.IsNotExist(err)
}

func beginInstall(paths installPaths) error {
	if err := os.MkdirAll(paths.DataRoot, 0750); err != nil {
		return fmt.Errorf("create resumable installation state: %w", err)
	}
	if hasInstallMarker(paths.MarkerPath) {
		return nil
	}
	if err := atomicWrite(paths.MarkerPath, []byte(installMarker), 0600); err != nil {
		return fmt.Errorf("mark installation in progress: %w", err)
	}
	return nil
}

type commandRunner struct{ output io.Writer }

func (r commandRunner) run(ctx context.Context, binary string, args ...string) error {
	fmt.Fprintf(r.output, "→ %s %s\n", binary, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout, cmd.Stderr = r.output, r.output
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(binary), err)
	}
	return nil
}

func ensureIdentity(ctx context.Context, runner commandRunner) error {
	if _, err := user.LookupGroup("wpx"); err != nil {
		if err := runner.run(ctx, "/usr/sbin/groupadd", "--system", "wpx"); err != nil {
			return err
		}
	}
	if _, err := user.Lookup("wpx"); err != nil {
		return runner.run(ctx, "/usr/sbin/useradd", "--system", "--gid", "wpx", "--home-dir", "/var/lib/wpx", "--shell", "/usr/sbin/nologin", "wpx")
	}
	return nil
}

func installExecutable(source string) error {
	if source == "" {
		var err error
		source, err = os.Executable()
		if err != nil {
			return err
		}
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open WPX executable: %w", err)
	}
	defer in.Close()
	target := "/usr/local/bin/wpx"
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wpx-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

func bootstrapToken() (string, string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(b)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

// WriteUnits reconciles the process boundary for both fresh installations and
// upgrades. The root broker cannot use NoNewPrivileges: APT deliberately drops
// its transport process to _apt and needs to manage that saved identity. The
// unprivileged web process retains the stricter setting and an empty capability
// set because it never invokes host package tools.
func WriteUnits(configPath string) error {
	brokerUnit := `[Unit]
Description=WPX privileged broker
After=local-fs.target

[Service]
Type=simple
User=root
Group=wpx
RuntimeDirectory=wpx
RuntimeDirectoryMode=0750
ExecStart=/usr/local/bin/wpx broker --config ` + configPath + `
Restart=on-failure
RestartSec=2s
PrivateTmp=true
ProtectHome=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LogNamespace=wpx

[Install]
WantedBy=multi-user.target
`
	webUnit := `[Unit]
Description=WPX web control plane
After=wpx-broker.service network-online.target
Requires=wpx-broker.service

[Service]
Type=simple
User=wpx
Group=wpx
ExecStart=/usr/local/bin/wpx serve --config ` + configPath + `
Restart=on-failure
RestartSec=2s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/wpx /var/log/wpx
CapabilityBoundingSet=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LogNamespace=wpx

[Install]
WantedBy=multi-user.target
`
	if err := atomicWrite("/etc/systemd/system/wpx-broker.service", []byte(brokerUnit), 0644); err != nil {
		return err
	}
	return atomicWrite("/etc/systemd/system/wpx.service", []byte(webUnit), 0644)
}
