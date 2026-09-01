package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type recordRunner struct {
	calls [][]string
	fail  string
}

func (r *recordRunner) Run(_ context.Context, executable string, args ...string) error {
	call := append([]string{executable}, args...)
	r.calls = append(r.calls, call)
	if executable == r.fail {
		return errors.New("injected failure")
	}
	return nil
}

type currentIdentity struct{}

func (currentIdentity) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{Name: "test", UID: os.Getuid(), GID: os.Getgid()}, nil
}

type fakePHP struct {
	socket string
}

type fakeDatabase struct{}

func (fakeDatabase) Ensure(context.Context, model.Site) (DatabaseCredentials, error) {
	return DatabaseCredentials{Name: "db", User: "user", Password: "secret", Host: "localhost"}, nil
}

type fakeWordPress struct {
	called       bool
	redisEnabled bool
}

type fakePython struct {
	socket string
}

func (p fakePython) Ensure(context.Context, model.Site, Identity, string, string) (string, error) {
	return p.socket, nil
}

func (w *fakeWordPress) Ensure(context.Context, model.Site, Identity, string, DatabaseCredentials) error {
	w.called = true
	return nil
}

func (w *fakeWordPress) EnableHTTPS(context.Context, model.Site, Identity, string) error {
	w.called = true
	return nil
}

func (w *fakeWordPress) ApplyRedis(_ context.Context, _ model.Site, _ Identity, _ string, enabled bool) error {
	w.called = true
	w.redisEnabled = enabled
	return nil
}

func (p fakePHP) Ensure(context.Context, model.Site, Identity, string) (string, error) {
	return p.socket, nil
}

func TestProvisionWordPressRunsApplicationInstall(t *testing.T) {
	host := testHost(t, &recordRunner{})
	wordpress := &fakeWordPress{}
	host.Database = fakeDatabase{}
	host.WordPress = wordpress
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if !wordpress.called {
		t.Fatal("WordPress application installer was not called")
	}
	content, err := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-example-com.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "try_files $uri $uri/ /index.php?$args;") {
		t.Fatalf("WordPress front controller missing: %s", content)
	}
}

func TestApplyPerformanceReconcilesHTTPAndTLSConfigurations(t *testing.T) {
	host := testHost(t, &recordRunner{})
	wordpress := &fakeWordPress{}
	host.WordPress = wordpress
	site := model.Site{
		ID: "example-com", Domain: "example.com", Kind: model.WordPress,
		PHPVersion: "8.4", Status: "active", TLSStatus: "active",
		RedisEnabled: true, FastCGICacheEnabled: true,
	}
	if err := host.ApplyPerformance(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if !wordpress.redisEnabled {
		t.Fatal("Redis desired state was not applied")
	}
	for _, name := range []string{"wpx-example-com.conf", "wpx-example-com-tls.conf"} {
		content, err := os.ReadFile(filepath.Join(host.NginxAvailable, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "fastcgi_cache WPX;") || !strings.Contains(string(content), "wordpress_logged_in") {
			t.Fatalf("cache policy missing from %s: %s", name, content)
		}
	}
}

func TestRenderWordPressWithoutFastCGICacheOmitsCachePolicy(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	configuration := renderPHP(site, "/srv/example/public", "/run/php/example.sock", true)
	if strings.Contains(configuration, "fastcgi_cache WPX") || strings.Contains(configuration, "wpx_skip_cache") {
		t.Fatalf("disabled FastCGI cache leaked configuration: %s", configuration)
	}
}

func TestRenderWordPressSubdomainMultisiteIncludesWildcardAndCoreRewrites(t *testing.T) {
	site := model.Site{ID: "network-example", Domain: "network.example.com", Kind: model.WordPress, PHPVersion: "8.4", WordPressMultisite: model.MultisiteSubdomains}
	configuration := renderPHP(site, "/srv/example/public", "/run/php/example.sock", true)
	for _, expected := range []string{"server_name network.example.com *.network.example.com;", "rewrite /wp-admin$", "rewrite ^(/[^/]+)?(/wp-.*) $2 last;"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("multisite configuration missing %q: %s", expected, configuration)
		}
	}
}

func testHost(t *testing.T, runner Runner) *Host {
	t.Helper()
	root := t.TempDir()
	return &Host{
		SiteRoot:         filepath.Join(root, "sites"),
		NginxAvailable:   filepath.Join(root, "nginx", "available"),
		NginxEnabled:     filepath.Join(root, "nginx", "enabled"),
		CertificateRoot:  filepath.Join(root, "certificates"),
		DataRoot:         filepath.Join(root, "data"),
		StagingAuthRoot:  filepath.Join(root, "staging-auth"),
		NginxLogRoot:     filepath.Join(root, "logs"),
		NginxSnippetRoot: filepath.Join(root, "snippets", "nginx"),
		PHPSnippetRoot:   filepath.Join(root, "snippets", "php"),
		Runner:           runner,
		Identities:       currentIdentity{},
		PHP:              fakePHP{socket: "/run/php/wpx-example-com.sock"},
		Python:           fakePython{socket: "/run/wpx-sites/example-com.sock"},
	}
}

func TestProvisionPythonUsesSocketActivatedUpstream(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Python}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-example-com.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "proxy_pass http://unix:/run/wpx-sites/example-com.sock:;") {
		t.Fatalf("Python socket upstream missing: %s", content)
	}
}

func TestProvisionPHPSiteUsesDedicatedSocket(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4"}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-example-com.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "fastcgi_pass unix:/run/php/wpx-example-com.sock;") {
		t.Fatalf("dedicated PHP socket missing: %s", content)
	}
}

func TestProvisionStaticSiteIsManagedAndIdempotent(t *testing.T) {
	runner := &recordRunner{}
	host := testHost(t, runner)
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}
	for i := 0; i < 2; i++ {
		if err := host.Provision(context.Background(), site); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(host.NginxAvailable, "wpx-example-com.conf")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), ownershipMarker) || !strings.Contains(string(content), "server_name example.com;") {
		t.Fatalf("unexpected nginx configuration: %s", content)
	}
	link, err := os.Readlink(filepath.Join(host.NginxEnabled, "wpx-example-com.conf"))
	if err != nil || link != configPath {
		t.Fatalf("unexpected enabled link %q: %v", link, err)
	}
	wantCalls := [][]string{
		{"/usr/sbin/nginx", "-t"}, {"/usr/bin/systemctl", "reload", "nginx.service"},
		{"/usr/sbin/nginx", "-t"}, {"/usr/bin/systemctl", "reload", "nginx.service"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected commands: %#v", runner.calls)
	}
}

func TestProvisionRefusesUnmanagedConfiguration(t *testing.T) {
	host := testHost(t, &recordRunner{})
	if err := os.MkdirAll(host.NginxAvailable, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.NginxAvailable, "wpx-example-com.conf")
	if err := os.WriteFile(path, []byte("# written by operator\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := host.Provision(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("expected unmanaged-file error, got %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "# written by operator\n" {
		t.Fatalf("operator file changed: %q", content)
	}
}

func TestProvisionRollsBackInvalidNginxChange(t *testing.T) {
	runner := &recordRunner{fail: "/usr/sbin/nginx"}
	host := testHost(t, runner)
	err := host.Provision(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:8080"})
	if err == nil {
		t.Fatal("expected nginx validation failure")
	}
	for _, path := range []string{
		filepath.Join(host.NginxAvailable, "wpx-example-com.conf"),
		filepath.Join(host.NginxEnabled, "wpx-example-com.conf"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failed activation left %s behind: %v", path, err)
		}
	}
}

func TestAccountNameIsStableAndOpaque(t *testing.T) {
	first := accountName("customer-example")
	if first != accountName("customer-example") || strings.Contains(first, "customer") || len(first) != 15 {
		t.Fatalf("unexpected account name %q", first)
	}
}
