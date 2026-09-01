package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type certificateRunner struct {
	host              *Host
	args              []string
	credentialsPath   string
	credentialsDuring string
}

func (r *certificateRunner) Run(_ context.Context, executable string, args ...string) error {
	if executable == "/usr/bin/certbot" {
		r.args = append([]string(nil), args...)
		domain := ""
		for i := range args {
			if args[i] == "--cert-name" && i+1 < len(args) {
				domain = args[i+1]
			} else if args[i] == "--domain" && i+1 < len(args) && domain == "" {
				domain = args[i+1]
			} else if args[i] == "--dns-cloudflare-credentials" && i+1 < len(args) {
				r.credentialsPath = args[i+1]
				content, err := os.ReadFile(r.credentialsPath)
				if err != nil {
					return err
				}
				r.credentialsDuring = string(content)
			}
		}
		root := filepath.Join(r.host.CertificateRoot, domain)
		if err := os.MkdirAll(root, 0755); err != nil {
			return err
		}
		for _, name := range []string{"fullchain.pem", "privkey.pem"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("test certificate"), 0600); err != nil {
				return err
			}
		}
	}
	return nil
}

type certificateEnvironmentRunner struct {
	host        *Host
	environment []string
	args        []string
}

func (r *certificateEnvironmentRunner) RunEnv(_ context.Context, environment []string, executable string, args ...string) error {
	r.environment = append([]string(nil), environment...)
	r.args = append([]string(nil), args...)
	if executable != "/usr/bin/certbot" {
		return nil
	}
	domain := ""
	for i := range args {
		if args[i] == "--cert-name" && i+1 < len(args) {
			domain = args[i+1]
		}
	}
	root := filepath.Join(r.host.CertificateRoot, domain)
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("test certificate"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func (r *certificateEnvironmentRunner) OutputEnv(context.Context, []string, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestIssueCertificateActivatesManagedTLSVirtualHost(t *testing.T) {
	runner := &certificateRunner{}
	host := testHost(t, runner)
	runner.host = host
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := host.IssueCertificate(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-example-com-tls.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"listen 443 ssl;", filepath.Join(host.CertificateRoot, "example.com", "fullchain.pem"),
		"server_name example.com;", ownershipMarker,
	} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("TLS configuration missing %q: %s", expected, content)
		}
	}
}

func TestIssueCloudflareDNSCertificateUsesTemporaryCredentialFile(t *testing.T) {
	runner := &certificateRunner{}
	host := testHost(t, runner)
	runner.host = host
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	provider := model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, Status: "active", ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"}
	if err := host.IssueDNSCertificate(context.Background(), site, provider, true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.args, " ")
	for _, expected := range []string{"--dns-cloudflare", "--domain example.com", "--domain *.example.com"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Certbot arguments missing %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, provider.APIToken) {
		t.Fatal("Cloudflare token leaked into process arguments")
	}
	if !strings.Contains(runner.credentialsDuring, provider.APIToken) {
		t.Fatal("Cloudflare credentials were unavailable to Certbot")
	}
	if _, err := os.Stat(runner.credentialsPath); !os.IsNotExist(err) {
		t.Fatalf("temporary credentials remain after issuance: %v", err)
	}
}

func TestIssueRoute53DNSCertificateUsesEnvironment(t *testing.T) {
	runner := &certificateRunner{}
	host := testHost(t, runner)
	runner.host = host
	environment := &certificateEnvironmentRunner{host: host}
	host.Environment = environment
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	provider := model.DNSProvider{Name: "Route 53", Kind: model.DNSRoute53, Status: "active", ZoneID: "Z123EXAMPLE", AccessKey: "AKIAEXAMPLEACCESS", SecretKey: "a-secret-access-key-that-is-long-enough", SessionToken: "temporary-session-token"}
	if err := host.IssueDNSCertificate(context.Background(), site, provider, false); err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(environment.args, " ")
	if !strings.Contains(joinedArgs, "--dns-route53") || strings.Contains(joinedArgs, provider.AccessKey) || strings.Contains(joinedArgs, provider.SecretKey) || strings.Contains(joinedArgs, provider.SessionToken) {
		t.Fatalf("unsafe Route 53 Certbot arguments: %s", joinedArgs)
	}
	joinedEnvironment := strings.Join(environment.environment, "\n")
	for _, expected := range []string{"AWS_ACCESS_KEY_ID=" + provider.AccessKey, "AWS_SECRET_ACCESS_KEY=" + provider.SecretKey, "AWS_SESSION_TOKEN=" + provider.SessionToken} {
		if !strings.Contains(joinedEnvironment, expected) {
			t.Fatalf("Certbot environment missing %q", expected)
		}
	}
}
