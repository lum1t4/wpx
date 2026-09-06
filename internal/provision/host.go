// Package provision turns a validated site model into host state. It is used
// only by the root broker; the web process cannot import privileges through it.
package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lum1t4/wpx/internal/model"
)

const ownershipMarker = "# Managed by WPX. Manual changes will be replaced.\n"
const phpOwnershipMarker = "; Managed by WPX. Manual changes will be replaced.\n"

// Runner deliberately accepts an executable and an argument vector rather than
// a command string. Provisioning input must never reach a shell interpreter.
type Runner interface {
	Run(context.Context, string, ...string) error
}

type OutputRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type EnvironmentRunner interface {
	RunEnv(context.Context, []string, string, ...string) error
	OutputEnv(context.Context, []string, string, ...string) ([]byte, error)
}

type InputRunner interface {
	RunInput(context.Context, io.Reader, string, ...string) error
}

type ExecRunner struct {
	Output io.Writer
}

type ExecOutputRunner struct{}

type ExecEnvironmentRunner struct{}

type ExecInputRunner struct{}

func (ExecInputRunner) RunInput(ctx context.Context, input io.Reader, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = input
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s with input: %w", filepath.Base(executable), err)
	}
	return nil
}

func (ExecOutputRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	output, err := cmd.CombinedOutput()
	if len(output) > 1<<20 {
		return nil, errors.New("command output exceeds 1 MiB")
	}
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return output, nil
}

func (ExecEnvironmentRunner) RunEnv(ctx context.Context, environment []string, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), environment...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return nil
}

func (ExecEnvironmentRunner) OutputEnv(ctx context.Context, environment []string, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), environment...)
	output, err := cmd.CombinedOutput()
	if len(output) > 4<<20 {
		return nil, errors.New("command output exceeds 4 MiB")
	}
	if err != nil {
		// Restic output can contain repository details. Keep it out of the
		// worker log and return only the executable and process failure.
		return nil, fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return output, nil
}

func (r ExecRunner) Run(ctx context.Context, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout = r.Output
	cmd.Stderr = r.Output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return nil
}

type Identity struct {
	Name string
	UID  int
	GID  int
}

type IdentityManager interface {
	Ensure(context.Context, model.Site, string) (Identity, error)
}

type PHPRuntime interface {
	Ensure(context.Context, model.Site, Identity, string) (string, error)
}

type DatabaseManager interface {
	Ensure(context.Context, model.Site) (DatabaseCredentials, error)
}

type WordPressManager interface {
	Ensure(context.Context, model.Site, Identity, string, DatabaseCredentials) error
	EnableHTTPS(context.Context, model.Site, Identity, string) error
	ApplyRedis(context.Context, model.Site, Identity, string, bool) error
}

type PythonRuntime interface {
	Ensure(context.Context, model.Site, Identity, string, string) (string, error)
}

type SystemIdentities struct {
	Runner Runner
}

func (m SystemIdentities) Ensure(ctx context.Context, site model.Site, home string) (Identity, error) {
	name := accountName(site.ID)
	account, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return Identity{}, fmt.Errorf("lookup site account: %w", err)
		}
		if err := m.Runner.Run(ctx, "/usr/sbin/useradd", "--system", "--user-group", "--home-dir", home, "--shell", "/usr/sbin/nologin", name); err != nil {
			return Identity{}, fmt.Errorf("create site account: %w", err)
		}
		account, err = user.Lookup(name)
		if err != nil {
			return Identity{}, fmt.Errorf("lookup new site account: %w", err)
		}
	}
	// The site group remains private to this account. Nginx joins it only to
	// traverse and serve this site's public files.
	if err := m.Runner.Run(ctx, "/usr/sbin/usermod", "--append", "--groups", name, "www-data"); err != nil {
		return Identity{}, fmt.Errorf("grant nginx access to site: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return Identity{}, fmt.Errorf("decode site uid: %w", err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return Identity{}, fmt.Errorf("decode site gid: %w", err)
	}
	return Identity{Name: name, UID: uid, GID: gid}, nil
}

// accountName is stable but does not embed a user-controlled domain in an OS
// identity. Twelve hash digits make collisions negligible at panel scale while
// keeping the name well below Linux's conservative account-name limits.
func accountName(siteID string) string {
	digest := sha256.Sum256([]byte(siteID))
	return fmt.Sprintf("wpx%x", digest[:6])
}

type Host struct {
	SiteRoot         string
	NginxAvailable   string
	NginxEnabled     string
	CertificateRoot  string
	DataRoot         string
	Runner           Runner
	Output           OutputRunner
	Environment      EnvironmentRunner
	Input            InputRunner
	ResticPath       string
	RclonePath       string
	StagingAuthRoot  string
	NginxLogRoot     string
	NginxSnippetRoot string
	PHPSnippetRoot   string
	Identities       IdentityManager
	PHP              PHPRuntime
	Database         DatabaseManager
	WordPress        WordPressManager
	Python           PythonRuntime

	mu       sync.Mutex
	backupMu sync.Mutex
}

func DefaultHost(siteRoot, dataRoot string) *Host {
	runner := ExecRunner{Output: os.Stderr}
	return &Host{
		SiteRoot:         siteRoot,
		NginxAvailable:   "/etc/nginx/sites-available",
		NginxEnabled:     "/etc/nginx/sites-enabled",
		CertificateRoot:  "/etc/letsencrypt/live",
		DataRoot:         dataRoot,
		Runner:           runner,
		Output:           ExecOutputRunner{},
		Environment:      ExecEnvironmentRunner{},
		Input:            ExecInputRunner{},
		ResticPath:       "/usr/local/lib/wpx/restic",
		RclonePath:       "/usr/local/lib/wpx/rclone",
		StagingAuthRoot:  "/etc/wpx/staging",
		NginxLogRoot:     "/var/log/nginx",
		NginxSnippetRoot: "/etc/wpx/snippets/nginx",
		PHPSnippetRoot:   "/etc/wpx/snippets/php",
		Identities:       SystemIdentities{Runner: runner},
		PHP:              &AptPHPRuntime{Runner: runner, ConfigRoot: "/etc/php", RunRoot: "/run/php", SnippetRoot: "/etc/wpx/snippets/php"},
		Database:         &MariaDB{SecretsRoot: filepath.Join(dataRoot, "secrets", "sites"), SQL: ExecSQL{}},
		WordPress:        &WPCLI{Runner: runner, Path: "/usr/local/lib/wpx/wp-cli.phar"},
		Python:           &SystemPython{Runner: runner, UnitRoot: "/etc/systemd/system", RunRoot: "/run/wpx-sites"},
	}
}

func (h *Host) Provision(ctx context.Context, site model.Site) error {
	if err := model.ValidateSite(site); err != nil {
		return fmt.Errorf("validate site: %w", err)
	}
	if err := h.validate(); err != nil {
		return err
	}
	// Nginx has one global configuration graph. Serialize mutations so one
	// request cannot validate or roll back another request's half-finished state.
	h.mu.Lock()
	defer h.mu.Unlock()

	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureDirectory(siteDir, 0750, identity); err != nil {
		return fmt.Errorf("prepare site directory: %w", err)
	}
	if err := ensureDirectory(publicDir, 0750, identity); err != nil {
		return fmt.Errorf("prepare public directory: %w", err)
	}
	if err := ensureACMEChallengeDirectories(publicDir, identity); err != nil {
		return fmt.Errorf("prepare ACME challenge directory: %w", err)
	}

	var nginxConfig string
	nginxSnippet, err := h.ensureNginxSnippet(site.ID)
	if err != nil {
		return err
	}
	switch site.Kind {
	case model.Static:
		if err := ensureWelcomePage(publicDir, identity); err != nil {
			return err
		}
		nginxConfig = renderStatic(site, publicDir)
	case model.ReverseProxy:
		nginxConfig = renderReverseProxy(site, publicDir)
	case model.PHP:
		if h.PHP == nil {
			return errors.New("PHP runtime manager is unavailable")
		}
		if err := ensureDirectory(filepath.Join(siteDir, "tmp"), 0700, identity); err != nil {
			return fmt.Errorf("prepare PHP temporary directory: %w", err)
		}
		socket, err := h.PHP.Ensure(ctx, site, identity, siteDir)
		if err != nil {
			return err
		}
		if err := ensurePHPWelcomePage(publicDir, identity); err != nil {
			return err
		}
		nginxConfig = renderPHP(site, publicDir, socket, false)
	case model.WordPress:
		if h.PHP == nil || h.Database == nil || h.WordPress == nil {
			return errors.New("WordPress provisioning dependencies are unavailable")
		}
		if err := ensureDirectory(filepath.Join(siteDir, "tmp"), 0700, identity); err != nil {
			return fmt.Errorf("prepare WordPress temporary directory: %w", err)
		}
		socket, err := h.PHP.Ensure(ctx, site, identity, siteDir)
		if err != nil {
			return err
		}
		database, err := h.Database.Ensure(ctx, site)
		if err != nil {
			return err
		}
		if err := h.WordPress.Ensure(ctx, site, identity, publicDir, database); err != nil {
			return err
		}
		nginxConfig = renderPHP(site, publicDir, socket, true)
	case model.Python:
		if h.Python == nil {
			return errors.New("Python runtime manager is unavailable")
		}
		socket, err := h.Python.Ensure(ctx, site, identity, siteDir, publicDir)
		if err != nil {
			return err
		}
		nginxConfig = renderPython(site, socket, publicDir)
	default:
		return fmt.Errorf("%s provisioning is not implemented yet", site.Kind)
	}
	nginxConfig, err = injectNginxSnippet(nginxConfig, site, nginxSnippet)
	if err != nil {
		return err
	}
	return h.activateNginx(ctx, site.ID, []byte(nginxConfig))
}

// ApplyPerformance reconciles the WordPress and Nginx sides from persisted
// desired state. Rebuilding complete managed vhosts makes retries safe and
// avoids fragile line-oriented edits to Nginx configuration.
func (h *Host) ApplyPerformance(ctx context.Context, site model.Site) error {
	if err := model.ValidateSite(site); err != nil {
		return fmt.Errorf("validate site: %w", err)
	}
	if site.Kind != model.WordPress || site.Status != "active" {
		return errors.New("performance settings require an active WordPress site")
	}
	if err := h.validate(); err != nil {
		return err
	}
	if h.WordPress == nil {
		return errors.New("WordPress runtime manager is unavailable")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	publicDir := filepath.Join(siteDir, "public")
	if err := h.WordPress.ApplyRedis(ctx, site, identity, publicDir, site.RedisEnabled); err != nil {
		return err
	}
	httpConfig := renderPHP(site, publicDir, filepath.Join("/run/php", "wpx-"+site.ID+".sock"), true)
	nginxSnippet, err := h.ensureNginxSnippet(site.ID)
	if err != nil {
		return err
	}
	httpConfig, err = injectNginxSnippet(httpConfig, site, nginxSnippet)
	if err != nil {
		return err
	}
	if err := h.activateNginx(ctx, site.ID, []byte(httpConfig)); err != nil {
		return err
	}
	if site.TLSStatus != "active" {
		return nil
	}
	tlsConfig, err := tlsify(httpConfig, site.Domain, h.CertificateRoot)
	if err != nil {
		return err
	}
	return h.activateNginxConfig(ctx, "wpx-"+site.ID+"-tls.conf", []byte(tlsConfig))
}

func ensurePHPWelcomePage(publicDir string, identity Identity) error {
	path := filepath.Join(publicDir, "index.php")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect PHP welcome page: %w", err)
	}
	content := []byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>PHP site ready</title><h1>PHP site ready</h1></html>\n")
	if err := atomicWrite(path, content, 0640); err != nil {
		return fmt.Errorf("write PHP welcome page: %w", err)
	}
	return os.Chown(path, identity.UID, identity.GID)
}

func (h *Host) validate() error {
	for name, path := range map[string]string{"site root": h.SiteRoot, "nginx available root": h.NginxAvailable, "nginx enabled root": h.NginxEnabled, "certificate root": h.CertificateRoot, "data root": h.DataRoot, "nginx snippet root": h.NginxSnippetRoot, "php snippet root": h.PHPSnippetRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return fmt.Errorf("%s must be an absolute, clean, non-root path", name)
		}
	}
	if h.Runner == nil || h.Identities == nil {
		return errors.New("provisioner dependencies are incomplete")
	}
	return nil
}

func ensureContained(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("derived path escapes trusted root")
	}
	return nil
}

func ensureDirectory(path string, mode os.FileMode, identity Identity) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed directory is not a real directory")
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return os.Chown(path, identity.UID, identity.GID)
}

// MkdirAll only applies ownership and the requested mode to its final path.
// Prepare both ACME directories explicitly: Certbot runs as root while Nginx
// reads the token as a member of the site's group, so an intermediate directory
// inherited from a restrictive broker umask would otherwise make every HTTP-01
// challenge fail with 403.
func ensureACMEChallengeDirectories(publicDir string, identity Identity) error {
	wellKnown := filepath.Join(publicDir, ".well-known")
	if err := ensureDirectory(wellKnown, 0750, identity); err != nil {
		return err
	}
	return ensureDirectory(filepath.Join(wellKnown, "acme-challenge"), 0750, identity)
}

func ensureWelcomePage(publicDir string, identity Identity) error {
	path := filepath.Join(publicDir, "index.html")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect welcome page: %w", err)
	}
	content := []byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Site ready</title><h1>Site ready</h1><p>Upload your files from WPX.</p></html>\n")
	if err := atomicWrite(path, content, 0640); err != nil {
		return fmt.Errorf("write welcome page: %w", err)
	}
	return os.Chown(path, identity.UID, identity.GID)
}

func renderStatic(site model.Site, publicDir string) string {
	return ownershipMarker + "server {\n" +
		"    listen 80;\n" +
		"    listen [::]:80;\n" +
		"    server_name " + site.Domain + ";\n" +
		nginxLogDirectives(site) +
		"    root " + publicDir + ";\n" +
		"    index index.html;\n" +
		"    location / { try_files $uri $uri/ =404; }\n" +
		"}\n"
}

func renderReverseProxy(site model.Site, publicDir string) string {
	return ownershipMarker + "server {\n" +
		"    listen 80;\n" +
		"    listen [::]:80;\n" +
		"    server_name " + site.Domain + ";\n" +
		nginxLogDirectives(site) +
		"    location ^~ /.well-known/acme-challenge/ { root " + publicDir + "; }\n" +
		"    location / {\n" +
		"        proxy_pass " + site.Upstream + ";\n" +
		"        proxy_http_version 1.1;\n" +
		"        proxy_set_header Host $host;\n" +
		"        proxy_set_header X-Real-IP $remote_addr;\n" +
		"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
		"        proxy_set_header X-Forwarded-Proto $scheme;\n" +
		"        proxy_set_header Upgrade $http_upgrade;\n" +
		"        proxy_set_header Connection \"upgrade\";\n" +
		"    }\n" +
		"}\n"
}

func renderPHP(site model.Site, publicDir, socket string, wordpress bool) string {
	fallback := "=404"
	if wordpress {
		fallback = "/index.php?$args"
	}
	stagingHeaders := ""
	if site.Environment == "staging" {
		stagingHeaders = "    auth_basic \"WPX staging\";\n" +
			"    auth_basic_user_file /etc/wpx/staging/" + site.ID + ".htpasswd;\n" +
			"    add_header X-Robots-Tag \"noindex, nofollow, noarchive\" always;\n"
	}
	cachePolicy := ""
	cacheFastCGI := ""
	if wordpress && site.FastCGICacheEnabled {
		cachePolicy = "    set $wpx_skip_cache 0;\n" +
			"    if ($request_method = POST) { set $wpx_skip_cache 1; }\n" +
			"    if ($query_string != \"\") { set $wpx_skip_cache 1; }\n" +
			"    if ($request_uri ~* \"/wp-admin/|/wp-login\\.php|/wp-cron\\.php|/wp-json/\") { set $wpx_skip_cache 1; }\n" +
			"    if ($http_cookie ~* \"wordpress_logged_in|comment_author|woocommerce_items_in_cart|wp_woocommerce_session\") { set $wpx_skip_cache 1; }\n"
		cacheFastCGI = "        fastcgi_cache WPX;\n" +
			"        fastcgi_cache_bypass $wpx_skip_cache;\n" +
			"        fastcgi_no_cache $wpx_skip_cache;\n" +
			"        fastcgi_cache_valid 200 301 302 10m;\n" +
			"        add_header X-WPX-Cache $upstream_cache_status always;\n"
	}
	serverName := site.Domain
	multisiteRewrites := ""
	if wordpress && site.WordPressMultisite != model.MultisiteDisabled {
		if site.WordPressMultisite == model.MultisiteSubdomains {
			serverName += " *." + site.Domain
		}
		// WordPress stores network media and administration routes below each
		// subsite path. These rewrites remove that virtual prefix only for core
		// resources when no real file exists, matching WordPress' Nginx guidance.
		multisiteRewrites = "    if (!-e $request_filename) {\n" +
			"        rewrite /wp-admin$ $scheme://$host$uri/ permanent;\n" +
			"        rewrite ^(/[^/]+)?(/wp-.*) $2 last;\n" +
			"        rewrite ^(/[^/]+)?(/.*\\.php) $2 last;\n" +
			"    }\n"
	}
	return ownershipMarker + "server {\n" +
		"    listen 80;\n" +
		"    listen [::]:80;\n" +
		"    server_name " + serverName + ";\n" +
		nginxLogDirectives(site) +
		stagingHeaders +
		cachePolicy +
		multisiteRewrites +
		"    root " + publicDir + ";\n" +
		"    index index.php index.html;\n" +
		"    location ^~ /.well-known/acme-challenge/ { root " + publicDir + "; }\n" +
		"    location / { try_files $uri $uri/ " + fallback + "; }\n" +
		"    location ~ \\.php$ {\n" +
		"        try_files $uri =404;\n" +
		"        include fastcgi_params;\n" +
		"        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n" +
		"        fastcgi_pass unix:" + socket + ";\n" +
		cacheFastCGI +
		"    }\n" +
		"    location ~ /\\. { deny all; }\n" +
		"}\n"
}

func renderPython(site model.Site, socket, publicDir string) string {
	return ownershipMarker + "server {\n" +
		"    listen 80;\n" +
		"    listen [::]:80;\n" +
		"    server_name " + site.Domain + ";\n" +
		nginxLogDirectives(site) +
		"    location ^~ /.well-known/acme-challenge/ { root " + publicDir + "; }\n" +
		"    location / {\n" +
		"        proxy_pass http://unix:" + socket + ":;\n" +
		"        proxy_http_version 1.1;\n" +
		"        proxy_set_header Host $host;\n" +
		"        proxy_set_header X-Real-IP $remote_addr;\n" +
		"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
		"        proxy_set_header X-Forwarded-Proto $scheme;\n" +
		"    }\n" +
		"}\n"
}

func nginxLogDirectives(site model.Site) string {
	return "    access_log /var/log/nginx/wpx-" + site.ID + "-access.log;\n" +
		"    error_log /var/log/nginx/wpx-" + site.ID + "-error.log warn;\n"
}

func (h *Host) activateNginx(ctx context.Context, siteID string, content []byte) error {
	return h.activateNginxConfig(ctx, "wpx-"+siteID+".conf", content)
}

func (h *Host) activateNginxConfig(ctx context.Context, name string, content []byte) error {
	if err := os.MkdirAll(h.NginxAvailable, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(h.NginxEnabled, 0755); err != nil {
		return err
	}
	available := filepath.Join(h.NginxAvailable, name)
	enabled := filepath.Join(h.NginxEnabled, name)
	previous, existed, err := managedFileState(available)
	if err != nil {
		return err
	}
	if err := atomicWrite(available, content, 0644); err != nil {
		return err
	}
	linkCreated := false
	if target, err := os.Readlink(enabled); err == nil {
		if target != available {
			h.restoreConfig(available, previous, existed, false, enabled)
			return errors.New("enabled nginx path points to an unmanaged target")
		}
	} else if os.IsNotExist(err) {
		if err := os.Symlink(available, enabled); err != nil {
			h.restoreConfig(available, previous, existed, false, enabled)
			return fmt.Errorf("enable nginx site: %w", err)
		}
		linkCreated = true
	} else {
		h.restoreConfig(available, previous, existed, false, enabled)
		return fmt.Errorf("inspect enabled nginx site: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		h.restoreConfig(available, previous, existed, linkCreated, enabled)
		return fmt.Errorf("validate nginx configuration: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return fmt.Errorf("reload nginx: %w", err)
	}
	return nil
}

func managedFileState(path string) ([]byte, bool, error) {
	return managedFileStateWithMarker(path, ownershipMarker)
}

func managedFileStateWithMarker(path, marker string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read managed file: %w", err)
	}
	if !strings.HasPrefix(string(content), marker) && !(marker == phpOwnershipMarker && strings.HasPrefix(string(content), ownershipMarker)) {
		return nil, false, errors.New("refuse to replace unmanaged file")
	}
	return content, true, nil
}

func (h *Host) restoreConfig(path string, previous []byte, existed, linkCreated bool, enabled string) {
	if linkCreated {
		_ = os.Remove(enabled)
	}
	if existed {
		_ = atomicWrite(path, previous, 0644)
	} else {
		_ = os.Remove(path)
	}
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wpx-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
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
	return os.Rename(name, path)
}
