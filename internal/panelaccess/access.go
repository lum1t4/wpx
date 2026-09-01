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

	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/model"
)

const ownershipMarker = "# Managed by WPX. Manual changes will be replaced.\n"

type Runner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args ...string) error {
	if err := exec.CommandContext(ctx, executable, args...).Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return nil
}

type Options struct {
	ConfigPath      string
	Mode            string
	Domain          string
	Runner          Runner
	NginxAvailable  string
	NginxEnabled    string
	CertificateRoot string
}

type Result struct {
	URL string
}

func Configure(ctx context.Context, options Options) (Result, error) {
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
	cfg, err := config.Load(options.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	switch options.Mode {
	case "local":
		cfg.ListenAddress = "127.0.0.1:9443"
		return Result{URL: "https://127.0.0.1:9443"}, saveAndRestart(ctx, options, cfg)
	case "public":
		cfg.ListenAddress = "0.0.0.0:9443"
		return Result{URL: "https://SERVER_IP:9443"}, saveAndRestart(ctx, options, cfg)
	case "tailscale":
		// Tailscale remains responsible for identity, certificates, and ACLs.
		// WPX only exposes its existing TLS listener to the local daemon.
		if err := options.Runner.Run(ctx, "/usr/bin/tailscale", "serve", "--bg", "--yes", "--https=443", "https+insecure://127.0.0.1:9443"); err != nil {
			return Result{}, fmt.Errorf("configure Tailscale Serve: %w", err)
		}
		cfg.ListenAddress = "127.0.0.1:9443"
		return Result{URL: "https://TAILSCALE_HOSTNAME"}, saveAndRestart(ctx, options, cfg)
	case "domain":
		return configureDomain(ctx, options, cfg)
	default:
		return Result{}, errors.New("access mode must be local, public, tailscale, or domain")
	}
}

func configureDomain(ctx context.Context, options Options, cfg config.Config) (Result, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(options.Domain), "."))
	if err := model.ValidateDomain(domain); err != nil {
		return Result{}, err
	}
	challengeRoot := filepath.Join(cfg.DataRoot, "panel-public")
	if err := os.MkdirAll(filepath.Join(challengeRoot, ".well-known", "acme-challenge"), 0755); err != nil {
		return Result{}, fmt.Errorf("prepare panel challenge root: %w", err)
	}
	configPath := filepath.Join(options.NginxAvailable, "wpx-panel.conf")
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

func saveAndRestart(ctx context.Context, options Options, cfg config.Config) error {
	previous, err := config.Load(options.ConfigPath)
	if err != nil {
		return err
	}
	if err := config.Save(options.ConfigPath, cfg); err != nil {
		return err
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "restart", "wpx.service"); err != nil {
		// Restore the readable previous configuration before returning. A second
		// restart is best-effort because the original service may still be alive.
		_ = config.Save(options.ConfigPath, previous)
		_ = options.Runner.Run(ctx, "/usr/bin/systemctl", "restart", "wpx.service")
		return fmt.Errorf("restart panel after access change: %w", err)
	}
	return nil
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
		if target != available {
			return fmt.Errorf("refuse to replace unexpected Nginx link %s", enabled)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("refuse to replace non-link %s", enabled)
	}
	return os.Symlink(available, enabled)
}

func panelHTTPConfig(domain, challengeRoot string) string {
	return ownershipMarker + "server {\n    listen 80;\n    listen [::]:80;\n    server_name " + domain + ";\n    location ^~ /.well-known/acme-challenge/ { root " + challengeRoot + "; }\n    location / { proxy_pass https://127.0.0.1:9443; proxy_ssl_verify off; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto $scheme; proxy_set_header X-Real-IP $remote_addr; }\n}\n"
}

func panelTLSConfig(domain, challengeRoot, certRoot string) string {
	return ownershipMarker + "server {\n    listen 80;\n    listen [::]:80;\n    server_name " + domain + ";\n    location ^~ /.well-known/acme-challenge/ { root " + challengeRoot + "; }\n    location / { return 301 https://$host$request_uri; }\n}\nserver {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n    server_name " + domain + ";\n    ssl_certificate " + filepath.Join(certRoot, "fullchain.pem") + ";\n    ssl_certificate_key " + filepath.Join(certRoot, "privkey.pem") + ";\n    location / { proxy_pass https://127.0.0.1:9443; proxy_ssl_verify off; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; proxy_set_header X-Real-IP $remote_addr; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; }\n}\n"
}
