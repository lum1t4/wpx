package panelaccess

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/config"
)

type recordingRunner struct {
	certificateRoot string
	commands        []string
}

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) error {
	r.commands = append(r.commands, executable+" "+strings.Join(args, " "))
	if executable != "/usr/bin/certbot" {
		return nil
	}
	domain := ""
	for index := range args {
		if args[index] == "--cert-name" && index+1 < len(args) {
			domain = args[index+1]
		}
	}
	root := filepath.Join(r.certificateRoot, domain)
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("certificate"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func testConfiguration(t *testing.T) (string, config.Config) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "etc", "config.json")
	cfg := config.Default()
	cfg.ListenAddress = "0.0.0.0:9443"
	cfg.StatePath = filepath.Join(root, "state.db")
	cfg.BrokerSocket = filepath.Join(root, "run", "broker.sock")
	cfg.DataRoot = filepath.Join(root, "data")
	cfg.SiteRoot = filepath.Join(root, "sites")
	cfg.RunRoot = filepath.Join(root, "run")
	cfg.TLSCertPath = filepath.Join(root, "tls", "panel.crt")
	cfg.TLSKeyPath = filepath.Join(root, "tls", "panel.key")
	cfg.SecretKeyPath = filepath.Join(root, "secret.key")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func TestDomainAccessCreatesTrustedProxyAndMovesPanelToLoopback(t *testing.T) {
	configPath, cfg := testConfiguration(t)
	root := t.TempDir()
	certificateRoot := filepath.Join(root, "certificates")
	runner := &recordingRunner{certificateRoot: certificateRoot}
	result, err := Configure(context.Background(), Options{
		ConfigPath: configPath, Mode: "domain", Domain: "Panel.Example.com.", Runner: runner,
		NginxAvailable: filepath.Join(root, "nginx", "available"), NginxEnabled: filepath.Join(root, "nginx", "enabled"), CertificateRoot: certificateRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "https://panel.example.com" {
		t.Fatalf("URL=%q", result.URL)
	}
	updated, err := config.Load(configPath)
	if err != nil || updated.ListenAddress != "127.0.0.1:9443" {
		t.Fatalf("configuration=%#v err=%v", updated, err)
	}
	nginxConfig, err := os.ReadFile(filepath.Join(root, "nginx", "available", "wpx-panel.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{ownershipMarker, "server_name panel.example.com;", "listen 443 ssl;", "proxy_pass https://127.0.0.1:9443;", filepath.Join(certificateRoot, "panel.example.com", "fullchain.pem")} {
		if !strings.Contains(string(nginxConfig), expected) {
			t.Fatalf("panel proxy missing %q: %s", expected, nginxConfig)
		}
	}
	commands := strings.Join(runner.commands, "\n")
	for _, expected := range []string{"/usr/sbin/nginx -t", "/usr/bin/certbot certonly", "--deploy-hook /usr/bin/systemctl reload nginx.service", "/usr/bin/systemctl restart wpx.service"} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("commands missing %q: %s", expected, commands)
		}
	}
	if cfg.ListenAddress == updated.ListenAddress {
		t.Fatal("test did not prove the listener changed")
	}
}

func TestTailscaleAccessUsesPrivateServeAndLoopback(t *testing.T) {
	configPath, _ := testConfiguration(t)
	runner := &recordingRunner{}
	if _, err := Configure(context.Background(), Options{ConfigPath: configPath, Mode: "tailscale", Runner: runner}); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.commands, "\n")
	if !strings.Contains(commands, "/usr/bin/tailscale serve --bg --yes --https=443 https+insecure://127.0.0.1:9443") {
		t.Fatalf("unexpected Tailscale command: %s", commands)
	}
	updated, err := config.Load(configPath)
	if err != nil || updated.ListenAddress != "127.0.0.1:9443" {
		t.Fatalf("configuration=%#v err=%v", updated, err)
	}
}

func TestDomainAccessRefusesUnmanagedNginxFile(t *testing.T) {
	configPath, _ := testConfiguration(t)
	root := t.TempDir()
	available := filepath.Join(root, "nginx", "available")
	if err := os.MkdirAll(available, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(available, "wpx-panel.conf"), []byte("unmanaged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Configure(context.Background(), Options{ConfigPath: configPath, Mode: "domain", Domain: "panel.example.com", Runner: &recordingRunner{}, NginxAvailable: available, NginxEnabled: filepath.Join(root, "nginx", "enabled"), CertificateRoot: filepath.Join(root, "certificates")})
	if err == nil || !strings.Contains(err.Error(), "refuse to replace unmanaged") {
		t.Fatalf("error=%v", err)
	}
}
