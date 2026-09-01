package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) IssueCertificate(ctx context.Context, site model.Site) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	if err := ensureDirectory(filepath.Join(publicDir, ".well-known", "acme-challenge"), 0750, identity); err != nil {
		return err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/certbot", "certonly", "--webroot", "--webroot-path", publicDir, "--domain", site.Domain, "--non-interactive", "--agree-tos", "--register-unsafely-without-email", "--keep-until-expiring", "--deploy-hook", "/usr/bin/systemctl reload nginx.service"); err != nil {
		return fmt.Errorf("request Let's Encrypt certificate: %w", err)
	}
	return h.activateCertificate(ctx, site, identity, publicDir)
}

// IssueDNSCertificate uses the provider's native Certbot plugin, so sites can
// receive certificates before their web traffic points at this host. Secrets
// never enter the argument vector: Route 53 uses the child environment and
// Cloudflare uses a short-lived root-only credentials file.
func (h *Host) IssueDNSCertificate(ctx context.Context, site model.Site, provider model.DNSProvider, wildcard bool) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := model.ValidateDNSProvider(provider); err != nil || provider.Status != "active" {
		return errors.New("active DNS provider is required")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	args := []string{"certonly", "--non-interactive", "--agree-tos", "--register-unsafely-without-email", "--keep-until-expiring", "--cert-name", site.Domain, "--domain", site.Domain}
	if wildcard {
		args = append(args, "--domain", "*."+site.Domain)
	}
	args = append(args, "--deploy-hook", "/usr/bin/systemctl reload nginx.service")
	switch provider.Kind {
	case model.DNSCloudflare:
		temporaryRoot := filepath.Join(h.DataRoot, "tmp")
		if err := ensureContained(h.DataRoot, temporaryRoot); err != nil {
			return err
		}
		if err := os.MkdirAll(temporaryRoot, 0700); err != nil {
			return fmt.Errorf("prepare certificate credentials: %w", err)
		}
		temporaryInfo, err := os.Lstat(temporaryRoot)
		if err != nil || !temporaryInfo.IsDir() || temporaryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("certificate credential directory is not trusted")
		}
		if err := os.Chmod(temporaryRoot, 0700); err != nil {
			return err
		}
		credentials, err := os.CreateTemp(temporaryRoot, "cloudflare-*.ini")
		if err != nil {
			return fmt.Errorf("create certificate credentials: %w", err)
		}
		credentialsPath := credentials.Name()
		defer os.Remove(credentialsPath)
		if err := credentials.Chmod(0600); err != nil {
			credentials.Close()
			return err
		}
		if _, err := credentials.WriteString("dns_cloudflare_api_token = " + provider.APIToken + "\n"); err != nil {
			credentials.Close()
			return fmt.Errorf("write certificate credentials: %w", err)
		}
		if err := credentials.Close(); err != nil {
			return err
		}
		args = append([]string{"certonly", "--dns-cloudflare", "--dns-cloudflare-credentials", credentialsPath, "--dns-cloudflare-propagation-seconds", "30"}, args[1:]...)
		if err := h.Runner.Run(ctx, "/usr/bin/certbot", args...); err != nil {
			return fmt.Errorf("request Let's Encrypt DNS certificate: %w", err)
		}
	case model.DNSRoute53:
		if h.Environment == nil {
			return errors.New("environment runner is unavailable")
		}
		environment := []string{"AWS_ACCESS_KEY_ID=" + provider.AccessKey, "AWS_SECRET_ACCESS_KEY=" + provider.SecretKey}
		if provider.SessionToken != "" {
			environment = append(environment, "AWS_SESSION_TOKEN="+provider.SessionToken)
		}
		args = append([]string{"certonly", "--dns-route53"}, args[1:]...)
		if err := h.Environment.RunEnv(ctx, environment, "/usr/bin/certbot", args...); err != nil {
			return fmt.Errorf("request Let's Encrypt DNS certificate: %w", err)
		}
	default:
		return errors.New("unsupported DNS certificate provider")
	}
	return h.activateCertificate(ctx, site, identity, publicDir)
}

func (h *Host) activateCertificate(ctx context.Context, site model.Site, identity Identity, publicDir string) error {
	certRoot := filepath.Join(h.CertificateRoot, site.Domain)
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		info, err := os.Stat(filepath.Join(certRoot, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("certificate output %s is unavailable", name)
		}
	}
	httpConfig, err := h.renderSiteConfig(site, publicDir)
	if err != nil {
		return err
	}
	tlsConfig, err := tlsify(httpConfig, site.Domain, h.CertificateRoot)
	if err != nil {
		return err
	}
	if err := h.activateNginxConfig(ctx, "wpx-"+site.ID+"-tls.conf", []byte(tlsConfig)); err != nil {
		return err
	}
	if site.Kind == model.WordPress {
		if h.WordPress == nil {
			return errors.New("WordPress runtime manager is unavailable")
		}
		if err := h.WordPress.EnableHTTPS(ctx, site, identity, publicDir); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) renderSiteConfig(site model.Site, publicDir string) (string, error) {
	var generated string
	switch site.Kind {
	case model.Static:
		generated = renderStatic(site, publicDir)
	case model.ReverseProxy:
		generated = renderReverseProxy(site, publicDir)
	case model.PHP:
		generated = renderPHP(site, publicDir, filepath.Join("/run/php", "wpx-"+site.ID+".sock"), false)
	case model.WordPress:
		generated = renderPHP(site, publicDir, filepath.Join("/run/php", "wpx-"+site.ID+".sock"), true)
	case model.Python:
		generated = renderPython(site, filepath.Join("/run/wpx-sites", site.ID+".sock"), publicDir)
	default:
		return "", errors.New("unsupported site kind")
	}
	path, err := h.ensureNginxSnippet(site.ID)
	if err != nil {
		return "", err
	}
	return injectNginxSnippet(generated, site, path)
}

func tlsify(httpConfig, domain, certificateRoot string) (string, error) {
	listen := "    listen 80;\n    listen [::]:80;\n"
	if !strings.Contains(httpConfig, listen) {
		return "", errors.New("generated HTTP configuration lacks listen directives")
	}
	tlsListen := "    listen 443 ssl;\n    listen [::]:443 ssl;\n" +
		"    ssl_certificate " + filepath.Join(certificateRoot, domain, "fullchain.pem") + ";\n" +
		"    ssl_certificate_key " + filepath.Join(certificateRoot, domain, "privkey.pem") + ";\n"
	return strings.Replace(httpConfig, listen, tlsListen, 1), nil
}
