// Package panelaccess owns the host-level boundary through which operators
// reach WPX. Access changes are deliberately CLI-only: they require root and
// cannot be triggered by a compromised browser session.
package panelaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/install"
	"github.com/lum1t4/wpx/internal/model"
)

const ownershipMarker = "# Managed by WPX. Manual changes will be replaced.\n"

type Runner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args ...string) error {
	command := exec.CommandContext(ctx, executable, args...)
	// Access is an interactive, root-only CLI operation. Preserve Nginx and
	// certbot diagnostics so an operator sees why a transition was rolled back.
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return nil
}

func (ExecRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, executable, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return output, nil
}

type Options struct {
	ConfigPath      string
	Mode            string
	Domain          string
	Runner          Runner
	NginxAvailable  string
	NginxEnabled    string
	CertificateRoot string
	ChallengeRoot   string
	TailscalePath   string
	ReadyCheck      func(context.Context, config.Config) error
}

type Result struct {
	URL string
}

func Configure(ctx context.Context, options Options) (result Result, err error) {
	if options.ConfigPath == "" {
		options.ConfigPath = "/etc/wpx/config.json"
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.NginxAvailable == "" {
		options.NginxAvailable = "/etc/nginx/sites-available"
	}
	if options.NginxEnabled == "" {
		options.NginxEnabled = "/etc/nginx/sites-enabled"
	}
	if options.CertificateRoot == "" {
		options.CertificateRoot = "/etc/letsencrypt/live"
	}
	if options.ChallengeRoot == "" {
		// Nginx must traverse this directory. DataRoot is private (0750) and
		// contains encryption keys, so ACME tokens must not live beneath it.
		options.ChallengeRoot = "/var/lib/wpx-acme"
	}
	if options.TailscalePath == "" {
		options.TailscalePath = "/usr/bin/tailscale"
	}
	if options.ReadyCheck == nil {
		options.ReadyCheck = install.WaitForPanel
	}
	cfg, err := config.Load(options.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	if options.Mode != "local" && options.Mode != "public" && options.Mode != "tailscale" && options.Mode != "domain" {
		return Result{}, errors.New("access mode must be local, public, tailscale, or domain")
	}
	if options.Mode == "domain" {
		options.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(options.Domain), "."))
		if err := model.ValidateDomain(options.Domain); err != nil {
			return Result{}, err
		}
	}
	proxy, err := snapshotProxy(options)
	if err != nil {
		return Result{}, err
	}
	// A failed certificate request or service restart must not replace a working
	// domain with the temporary HTTP challenge server. Rollback gets its own
	// timeout: a cancelled CLI operation still owes the host its old access path.
	defer func() {
		if err != nil && proxy.changed {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if rollbackErr := proxy.restore(rollbackCtx, options); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("restore previous panel proxy: %w", rollbackErr))
			}
		}
	}()
	if options.Mode != "tailscale" {
		removed, removeErr := removePanelTailscale(ctx, options)
		if removeErr != nil {
			return Result{}, removeErr
		}
		if removed {
			defer func() {
				if err != nil {
					rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
					defer cancel()
					if rollbackErr := options.Runner.Run(rollbackCtx, options.TailscalePath, "serve", "--bg", "--yes", "--https=443", tailscaleTarget); rollbackErr != nil {
						err = errors.Join(err, fmt.Errorf("restore previous Tailscale Serve route: %w", rollbackErr))
					}
				}
			}()
		}
	}
	switch options.Mode {
	case "local":
		cfg.ListenAddress = "127.0.0.1:9443"
		return Result{URL: "https://127.0.0.1:9443"}, configureListener(ctx, options, cfg, proxy)
	case "public":
		cfg.ListenAddress = "0.0.0.0:9443"
		return Result{URL: "https://SERVER_IP:9443"}, configureListener(ctx, options, cfg, proxy)
	case "tailscale":
		alreadyConfigured, inspectErr := inspectTailscale(ctx, options)
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		if !alreadyConfigured {
			if err := options.Runner.Run(ctx, options.TailscalePath, "serve", "--bg", "--yes", "--https=443", tailscaleTarget); err != nil {
				return Result{}, fmt.Errorf("configure Tailscale Serve: %w", err)
			}
			defer func() {
				if err != nil {
					rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
					defer cancel()
					if rollbackErr := options.Runner.Run(rollbackCtx, options.TailscalePath, "serve", "--bg", "--yes", "--https=443", "--set-path=/", "off"); rollbackErr != nil {
						err = errors.Join(err, fmt.Errorf("remove newly created Tailscale Serve route: %w", rollbackErr))
					}
				}
			}()
		}
		cfg.ListenAddress = "127.0.0.1:9443"
		return Result{URL: "https://TAILSCALE_HOSTNAME"}, configureListener(ctx, options, cfg, proxy)
	case "domain":
		return configureDomain(ctx, options, cfg, proxy)
	default:
		return Result{}, errors.New("access mode must be local, public, tailscale, or domain")
	}
}

func configureDomain(ctx context.Context, options Options, cfg config.Config, proxy *proxySnapshot) (Result, error) {
	domain := options.Domain
	challengeRoot := options.ChallengeRoot
	if err := os.MkdirAll(filepath.Join(challengeRoot, ".well-known", "acme-challenge"), 0755); err != nil {
		return Result{}, fmt.Errorf("prepare panel challenge root: %w", err)
	}
	// Root may invoke the CLI with a restrictive umask. These directories contain
	// only public ACME tokens, and Nginx must traverse them regardless of umask.
	for _, path := range []string{challengeRoot, filepath.Join(challengeRoot, ".well-known"), filepath.Join(challengeRoot, ".well-known", "acme-challenge")} {
		if err := os.Chmod(path, 0755); err != nil {
			return Result{}, fmt.Errorf("make panel challenge directory readable: %w", err)
		}
	}
	configPath := filepath.Join(options.NginxAvailable, "wpx-panel.conf")
	proxy.changed = true
	if err := writeOwned(configPath, []byte(panelHTTPConfig(domain, challengeRoot))); err != nil {
		return Result{}, err
	}
	if err := activate(configPath, filepath.Join(options.NginxEnabled, "wpx-panel.conf")); err != nil {
		return Result{}, err
	}
	if err := options.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return Result{}, fmt.Errorf("validate panel proxy: %w", err)
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return Result{}, fmt.Errorf("activate panel proxy: %w", err)
	}
	if err := options.Runner.Run(ctx, "/usr/bin/certbot", "certonly", "--webroot", "--webroot-path", challengeRoot, "--cert-name", domain, "--domain", domain, "--non-interactive", "--agree-tos", "--register-unsafely-without-email", "--keep-until-expiring", "--deploy-hook", "/usr/bin/systemctl reload nginx.service"); err != nil {
		return Result{}, fmt.Errorf("request panel certificate: %w", err)
	}
	certRoot := filepath.Join(options.CertificateRoot, domain)
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		if info, err := os.Stat(filepath.Join(certRoot, name)); err != nil || !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("panel certificate output %s is unavailable", name)
		}
	}
	if err := writeOwned(configPath, []byte(panelTLSConfig(domain, challengeRoot, certRoot))); err != nil {
		return Result{}, err
	}
	if err := options.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return Result{}, fmt.Errorf("validate trusted panel proxy: %w", err)
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return Result{}, fmt.Errorf("activate trusted panel proxy: %w", err)
	}
	cfg.ListenAddress = "127.0.0.1:9443"
	if err := saveAndRestart(ctx, options, cfg); err != nil {
		return Result{}, err
	}
	return Result{URL: "https://" + domain}, nil
}

func configureListener(ctx context.Context, options Options, cfg config.Config, proxy *proxySnapshot) error {
	// Binding the Go process to loopback is not private while its old public
	// Nginx proxy is still enabled. Keep the available file for operator recovery;
	// remove only the exact WPX link whose ownership was checked before mutation.
	if proxy.linkTarget != "" {
		proxy.changed = true
		if err := os.Remove(proxy.enabled); err != nil {
			return fmt.Errorf("disable panel proxy: %w", err)
		}
		if err := reloadNginx(ctx, options.Runner); err != nil {
			return fmt.Errorf("disable panel proxy: %w", err)
		}
	}
	return saveAndRestart(ctx, options, cfg)
}

func saveAndRestart(ctx context.Context, options Options, cfg config.Config) error {
	previous, err := config.Load(options.ConfigPath)
	if err != nil {
		return err
	}
	if err := config.Save(options.ConfigPath, cfg); err != nil {
		return errors.Join(err, restoreConfig(ctx, options, previous))
	}
	if err := restartPanel(ctx, options, cfg); err != nil {
		return errors.Join(fmt.Errorf("restart panel after access change: %w", err), restoreConfig(ctx, options, previous))
	}
	return nil
}

func restoreConfig(ctx context.Context, options Options, previous config.Config) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := config.Save(options.ConfigPath, previous); err != nil {
		return fmt.Errorf("restore previous panel configuration: %w", err)
	}
	if err := restartPanel(rollbackCtx, options, previous); err != nil {
		return fmt.Errorf("restart previous panel configuration: %w", err)
	}
	return nil
}

func restartPanel(ctx context.Context, options Options, cfg config.Config) error {
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "restart", "wpx.service"); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return options.ReadyCheck(readyCtx, cfg)
}

func writeOwned(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.HasPrefix(string(existing), ownershipMarker) {
			return fmt.Errorf("refuse to replace unmanaged Nginx configuration %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wpx-panel-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0644); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func activate(available, enabled string) error {
	if err := os.MkdirAll(filepath.Dir(enabled), 0755); err != nil {
		return err
	}
	if target, err := os.Readlink(enabled); err == nil {
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(enabled), resolved)
		}
		if filepath.Clean(resolved) != available {
			return fmt.Errorf("refuse to replace unexpected Nginx link %s", enabled)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("refuse to replace non-link %s", enabled)
	}
	return os.Symlink(available, enabled)
}

func panelHTTPConfig(domain, challengeRoot string) string {
	return ownershipMarker + fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location ^~ /.well-known/acme-challenge/ {
        root %s;
    }

    location / { return 404; }
}
`, domain, challengeRoot)
}

func panelTLSConfig(domain, challengeRoot, certRoot string) string {
	return ownershipMarker + fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location ^~ /.well-known/acme-challenge/ {
        root %s;
    }

    location / { return 301 https://$host$request_uri; }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name %s;
    ssl_certificate %s;
    ssl_certificate_key %s;

    location / {
        proxy_pass https://127.0.0.1:9443;
        proxy_ssl_verify off;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
`, domain, challengeRoot, domain, filepath.Join(certRoot, "fullchain.pem"), filepath.Join(certRoot, "privkey.pem"))
}
