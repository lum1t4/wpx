package panelaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/config"
)

type recordingRunner struct {
	certificateRoot string
	commands        []string
	fail            func(string, int) error
	status          string
}

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) error {
	command := executable + " " + strings.Join(args, " ")
	r.commands = append(r.commands, command)
	if r.fail != nil {
		if err := r.fail(command, len(r.commands)); err != nil {
			return err
		}
	}
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

func (r *recordingRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	if err := r.Run(ctx, executable, args...); err != nil {
		return nil, err
	}
	if r.status == "" {
		return []byte("{}"), nil
	}
	return []byte(r.status), nil
}

func readyPanel(context.Context, config.Config) error { return nil }

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
		ReadyCheck:     readyPanel,
		ChallengeRoot:  filepath.Join(root, "public-challenges"),
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
	root := t.TempDir()
	if _, err := Configure(context.Background(), Options{ConfigPath: configPath, Mode: "tailscale", Runner: runner, ReadyCheck: readyPanel, NginxAvailable: filepath.Join(root, "available"), NginxEnabled: filepath.Join(root, "enabled")}); err != nil {
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

func accessOptions(t *testing.T) (Options, *recordingRunner, config.Config) {
	t.Helper()
	path, cfg := testConfiguration(t)
	root := t.TempDir()
	runner := &recordingRunner{certificateRoot: filepath.Join(root, "certificates")}
	return Options{
		ConfigPath: path, Mode: "domain", Domain: "panel.example.com", Runner: runner,
		NginxAvailable: filepath.Join(root, "available"), NginxEnabled: filepath.Join(root, "enabled"),
		CertificateRoot: runner.certificateRoot, ReadyCheck: readyPanel,
		ChallengeRoot: filepath.Join(root, "public-challenges"),
	}, runner, cfg
}

func existingProxy(t *testing.T, options Options, enabled bool) []byte {
	t.Helper()
	content := []byte(panelTLSConfig("previous.example.com", "/previous/challenges", "/previous/certs"))
	available := filepath.Join(options.NginxAvailable, "wpx-panel.conf")
	if err := writeOwned(available, content); err != nil {
		t.Fatal(err)
	}
	if enabled {
		if err := activate(available, filepath.Join(options.NginxEnabled, "wpx-panel.conf")); err != nil {
			t.Fatal(err)
		}
	}
	return content
}

func assertProxy(t *testing.T, options Options, content []byte, enabled bool) {
	t.Helper()
	available := filepath.Join(options.NginxAvailable, "wpx-panel.conf")
	actual, err := os.ReadFile(available)
	if err != nil || string(actual) != string(content) {
		t.Fatalf("proxy changed: error=%v content=%s", err, actual)
	}
	target, err := os.Readlink(filepath.Join(options.NginxEnabled, "wpx-panel.conf"))
	if enabled && (err != nil || target != available) {
		t.Fatalf("proxy link changed: target=%q err=%v", target, err)
	}
	if !enabled && !os.IsNotExist(err) {
		t.Fatalf("proxy link should be absent, target=%q err=%v", target, err)
	}
}

func TestFailedDomainChangeRestoresPreviousProxyAndListener(t *testing.T) {
	for _, failure := range []string{"certificate", "nginx validation", "nginx reload", "trusted proxy validation", "trusted proxy reload", "restart", "readiness"} {
		t.Run(failure, func(t *testing.T) {
			options, runner, cfg := accessOptions(t)
			previous := existingProxy(t, options, true)
			failed := false
			runner.fail = func(command string, count int) error {
				matches := failure == "certificate" && strings.HasPrefix(command, "/usr/bin/certbot ") ||
					failure == "nginx validation" && command == "/usr/sbin/nginx -t" ||
					failure == "nginx reload" && command == "/usr/bin/systemctl reload nginx.service" ||
					failure == "trusted proxy validation" && count == 4 ||
					failure == "trusted proxy reload" && count == 5 ||
					failure == "restart" && command == "/usr/bin/systemctl restart wpx.service"
				if matches && !failed {
					failed = true
					return errors.New("injected " + failure)
				}
				return nil
			}
			options.ReadyCheck = func(context.Context, config.Config) error {
				if failure == "readiness" && !failed {
					failed = true
					return errors.New("injected readiness")
				}
				return nil
			}
			_, err := Configure(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), "injected "+failure) {
				t.Fatalf("error=%v", err)
			}
			assertProxy(t, options, previous, true)
			actual, err := config.Load(options.ConfigPath)
			if err != nil || actual != cfg {
				t.Fatalf("configuration changed: %#v err=%v", actual, err)
			}
			commands := strings.Join(runner.commands, "\n")
			if !strings.HasSuffix(commands, "/usr/sbin/nginx -t\n/usr/bin/systemctl reload nginx.service") {
				t.Fatalf("previous proxy was not reloaded: %s", commands)
			}
		})
	}
}

func TestFailedFirstDomainConfigurationRemovesTemporaryProxy(t *testing.T) {
	options, runner, _ := accessOptions(t)
	runner.fail = func(command string, _ int) error {
		if strings.HasPrefix(command, "/usr/bin/certbot ") {
			return errors.New("certificate request failed")
		}
		return nil
	}
	if _, err := Configure(context.Background(), options); err == nil {
		t.Fatal("expected certificate error")
	}
	for _, root := range []string{options.NginxAvailable, options.NginxEnabled} {
		if _, err := os.Lstat(filepath.Join(root, "wpx-panel.conf")); !os.IsNotExist(err) {
			t.Fatalf("temporary proxy left behind: %v", err)
		}
	}
}

func TestListenerModesDisableOnlyWPXProxy(t *testing.T) {
	for _, mode := range []string{"local", "public", "tailscale"} {
		t.Run(mode, func(t *testing.T) {
			options, _, _ := accessOptions(t)
			options.Mode = mode
			previous := existingProxy(t, options, true)
			unrelated := filepath.Join(options.NginxEnabled, "customer.conf")
			if err := os.WriteFile(unrelated, []byte("customer configuration\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Configure(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			assertProxy(t, options, previous, false)
			content, err := os.ReadFile(unrelated)
			if err != nil || string(content) != "customer configuration\n" {
				t.Fatalf("unrelated vhost changed: %q err=%v", content, err)
			}
		})
	}
}

func TestLocalFailureRestoresPublicProxy(t *testing.T) {
	options, runner, cfg := accessOptions(t)
	options.Mode = "local"
	previous := existingProxy(t, options, true)
	failed := false
	runner.fail = func(command string, _ int) error {
		if command == "/usr/bin/systemctl restart wpx.service" && !failed {
			failed = true
			return errors.New("restart failed")
		}
		return nil
	}
	if _, err := Configure(context.Background(), options); err == nil {
		t.Fatal("expected restart failure")
	}
	assertProxy(t, options, previous, true)
	actual, err := config.Load(options.ConfigPath)
	if err != nil || actual != cfg {
		t.Fatalf("previous listener not restored: %#v err=%v", actual, err)
	}
}

func TestUnexpectedProxyLinkIsNotModified(t *testing.T) {
	options, runner, _ := accessOptions(t)
	previous := existingProxy(t, options, false)
	if err := os.MkdirAll(options.NginxEnabled, 0755); err != nil {
		t.Fatal(err)
	}
	enabled := filepath.Join(options.NginxEnabled, "wpx-panel.conf")
	if err := os.Symlink("unrelated.conf", enabled); err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(context.Background(), options); err == nil {
		t.Fatal("expected ownership error")
	}
	actual, err := os.ReadFile(filepath.Join(options.NginxAvailable, "wpx-panel.conf"))
	if err != nil || string(actual) != string(previous) || len(runner.commands) != 0 {
		t.Fatalf("mutated state before ownership checks: err=%v commands=%v", err, runner.commands)
	}
	if target, err := os.Readlink(enabled); err != nil || target != "unrelated.conf" {
		t.Fatalf("unrelated link changed: %q err=%v", target, err)
	}
}

func TestRollbackFailureIsReported(t *testing.T) {
	options, runner, _ := accessOptions(t)
	existingProxy(t, options, true)
	reloads := 0
	runner.fail = func(command string, _ int) error {
		if strings.HasPrefix(command, "/usr/bin/certbot ") {
			return errors.New("original certificate failure")
		}
		if command == "/usr/bin/systemctl reload nginx.service" {
			reloads++
			if reloads == 2 {
				return errors.New("rollback reload failure")
			}
		}
		return nil
	}
	_, err := Configure(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "original certificate failure") || !strings.Contains(err.Error(), "rollback reload failure") {
		t.Fatalf("error=%v", err)
	}
}

func TestTemporaryHTTPProxyDoesNotExposePanel(t *testing.T) {
	content := panelHTTPConfig("panel.example.com", "/var/lib/wpx/panel-public")
	if strings.Contains(content, "proxy_pass") || !strings.Contains(content, "return 404") {
		t.Fatalf("temporary HTTP server exposes the panel: %s", content)
	}
}

func TestDomainChallengesDoNotExposePrivatePanelData(t *testing.T) {
	options, runner, cfg := accessOptions(t)
	if err := os.MkdirAll(cfg.DataRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(cfg.DataRoot)
	if err != nil || privateInfo.Mode().Perm() != 0750 {
		t.Fatalf("private panel data permissions changed: %v err=%v", privateInfo, err)
	}
	for _, path := range []string{options.ChallengeRoot, filepath.Join(options.ChallengeRoot, ".well-known"), filepath.Join(options.ChallengeRoot, ".well-known", "acme-challenge")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("public challenge directory is inaccessible: %s info=%v err=%v", path, info, err)
		}
	}
	commands := strings.Join(runner.commands, "\n")
	if !strings.Contains(commands, "--webroot-path "+options.ChallengeRoot) || strings.Contains(commands, cfg.DataRoot) {
		t.Fatalf("certbot must not serve private panel data: %s", commands)
	}
	content, err := os.ReadFile(filepath.Join(options.NginxAvailable, "wpx-panel.conf"))
	if err != nil || !strings.Contains(string(content), "root "+options.ChallengeRoot+";") || strings.Contains(string(content), cfg.DataRoot) {
		t.Fatalf("Nginx must not serve private panel data: %s err=%v", content, err)
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
