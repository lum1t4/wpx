package provision

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type hostingRunner struct{ commands [][]string }

func (r *hostingRunner) Run(_ context.Context, executable string, args ...string) error {
	r.commands = append(r.commands, append([]string{executable}, args...))
	return nil
}

type hostingIdentity struct{}

func (hostingIdentity) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{Name: "wpxsite", UID: 123, GID: 123}, nil
}

func TestConfinedNodeEntrypointRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.js")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(public, "server.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedExistingFile(public, "server.js"); err == nil {
		t.Fatal("accepted an entrypoint symlink outside the site")
	}
}
func TestSystemdArgumentsRemainOneDirective(t *testing.T) {
	got := joinSystemdArguments([]string{"/usr/bin/node", "server.js", "value with spaces", "$(touch /tmp/x)", "100%"})
	if strings.ContainsAny(got, "\r\n") {
		t.Fatal("argument injected a directive")
	}
	if !strings.Contains(got, `"$$(touch /tmp/x)"`) || !strings.Contains(got, "100%%") {
		t.Fatalf("arguments were not safely quoted: %s", got)
	}
}

func TestApplyNodeRuntimeWritesConfinedService(t *testing.T) {
	root := t.TempDir()
	siteRoot := filepath.Join(root, "sites")
	unitRoot := filepath.Join(root, "units")
	nodeRoot := filepath.Join(root, "node")
	for _, dir := range []string{filepath.Join(siteRoot, "node-app", "public"), unitRoot, filepath.Join(nodeRoot, "node-v"+nodeRuntimeVersion, "bin")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "node-app", "public", "server.js"), []byte("listen"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeRoot, "node-v"+nodeRuntimeVersion, "bin", "node"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeRoot, "node-v"+nodeRuntimeVersion, ".wpx-node-runtime"), []byte(nodeRuntimeVersion+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	runner := &hostingRunner{}
	host := &Host{SiteRoot: siteRoot, HostingUnitRoot: unitRoot, NodeInstallRoot: nodeRoot, Runner: runner, Identities: hostingIdentity{}}
	site := model.Site{ID: "node-app", Domain: "node.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:" + strconv.Itoa(port)}
	runtime := model.NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Arguments: []string{"$TOKEN"}, Port: port, NodeVersion: "24.20.0"}
	if err := host.ApplyNodeRuntime(context.Background(), site, runtime); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(unitRoot, "wpx-node-node-app.service"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, required := range []string{"User=wpxsite", "IPAddressDeny=any", "IPAddressAllow=localhost", "$$TOKEN", filepath.Join(siteRoot, "node-app", "public", "server.js")} {
		if !strings.Contains(content, required) {
			t.Fatalf("unit lacks %q:\n%s", required, content)
		}
	}
	if len(runner.commands) < 2 {
		t.Fatalf("systemd was not activated: %#v", runner.commands)
	}
}
func TestFTPAuthUpsertDoesNotDuplicateUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users")
	if err := upsertFTPAuth(path, "deploy", "deploy:first:1:1::/one:/usr/sbin/nologin"); err != nil {
		t.Fatal(err)
	}
	if err := upsertFTPAuth(path, "deploy", "deploy:second:1:1::/one:/usr/sbin/nologin"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "deploy:") != 1 || strings.Contains(string(raw), "first") {
		t.Fatalf("unexpected auth file: %s", raw)
	}
}
