package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type operatorAlertRunner struct {
	calls      []string
	activeSwap string
}

func (r *operatorAlertRunner) Run(_ context.Context, executable string, args ...string) error {
	r.calls = append(r.calls, executable+" "+strings.Join(args, " "))
	if executable == "/usr/bin/fallocate" {
		size, _ := strconv.Atoi(strings.TrimSuffix(args[1], "M"))
		file, err := os.Create(args[2])
		if err != nil {
			return err
		}
		if err = file.Truncate(int64(size) * 1024 * 1024); err != nil {
			return err
		}
		return file.Close()
	}
	return nil
}
func (r *operatorAlertRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, executable+" "+strings.Join(args, " "))
	if executable == "/sbin/swapon" {
		return []byte(r.activeSwap), nil
	}
	if executable == "/usr/bin/systemctl" {
		return []byte("ActiveState=failed\nSubState=failed\nNRestarts=2\nExecMainStatus=1\n"), nil
	}
	if executable == "/usr/bin/journalctl" {
		return []byte(strings.Repeat("ordinary bounded diagnostic line\n", 300) + "token=must-not-leave-host\n"), nil
	}
	return nil, errors.New("unexpected command")
}

func TestEnsureOperatorSwapCreatesOnlyManagedDataRootFile(t *testing.T) {
	root := t.TempDir()
	runner := &operatorAlertRunner{}
	host := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	host.Runner = runner
	host.Output = runner
	path, created, err := host.EnsureOperatorSwap(context.Background(), 512)
	if err != nil || !created {
		t.Fatalf("created=%t path=%q err=%v", created, path, err)
	}
	if path != filepath.Join(root, "data", "swap", "wpx.swap") {
		t.Fatalf("unsafe path %q", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 512*1024*1024 {
		t.Fatalf("swap file %#v err=%v", info, err)
	}
	runner.activeSwap = path + "\n"
	before := len(runner.calls)
	_, created, err = host.EnsureOperatorSwap(context.Background(), 512)
	if err != nil || created || len(runner.calls) != before+1 {
		t.Fatalf("repeat changed host: created=%t calls=%v err=%v", created, runner.calls[before:], err)
	}
}

func TestEnsureOperatorSwapRefusesUnmanagedAndSymlinkedPaths(t *testing.T) {
	root := t.TempDir()
	runner := &operatorAlertRunner{}
	host := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	host.Runner = runner
	host.Output = runner
	directory := filepath.Join(root, "data", "swap")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "wpx.swap"), []byte("unmanaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.EnsureOperatorSwap(context.Background(), 512); err == nil {
		t.Fatal("unmanaged file accepted")
	}
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, directory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.EnsureOperatorSwap(context.Background(), 512); err == nil {
		t.Fatal("symlinked directory accepted")
	}
}

func TestEnsureOperatorSwapRequiresRunners(t *testing.T) {
	host := DefaultHost(filepath.Join(t.TempDir(), "sites"), filepath.Join(t.TempDir(), "data"))
	host.Runner, host.Output = nil, nil
	if _, _, err := host.EnsureOperatorSwap(context.Background(), 512); err == nil {
		t.Fatal("missing dependencies caused no error")
	}
}

func TestCertificatePathAllowsConfinedCertbotArchiveSymlink(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	archive := filepath.Join(root, "archive", "example.com")
	if err := os.MkdirAll(filepath.Join(live, "example.com"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(archive, "cert1.pem")
	if err := os.WriteFile(target, []byte("certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(live, "example.com", "fullchain.pem")
	if err := os.Symlink(filepath.Join("..", "..", "archive", "example.com", "cert1.pem"), link); err != nil {
		t.Fatal(err)
	}
	resolved, err := confinedCertificatePath(live, link)
	if err != nil || resolved != target {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	escape := filepath.Join(live, "example.com", "escape.pem")
	if err := os.Symlink("/etc/passwd", escape); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedCertificatePath(live, escape); err == nil {
		t.Fatal("escaping certificate symlink accepted")
	}
}

func TestOperatorDiagnosticsBoundsCrashOutput(t *testing.T) {
	root := t.TempDir()
	runner := &operatorAlertRunner{}
	host := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	host.Runner = runner
	host.Output = runner
	result, err := host.OperatorDiagnostics(context.Background(), nil, nil)
	if err != nil || len(result.Services) != 5 {
		t.Fatalf("diagnostics=%#v err=%v", result, err)
	}
	for _, service := range result.Services {
		joined := strings.Join(service.Diagnostics, "\n")
		if service.Active != "failed" || len(service.Diagnostics) > 20 || len(joined) > 4096+512 || strings.Contains(joined, "must-not-leave-host") {
			t.Fatalf("unbounded service diagnostics: %#v", service)
		}
	}
}

func TestOperatorDiagnosticsIncludesManagedRuntimeServices(t *testing.T) {
	root := t.TempDir()
	runner := &operatorAlertRunner{}
	host := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	host.Runner = runner
	host.Output = runner
	sites := []model.Site{{ID: "php-site", Domain: "php.example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "active"}, {ID: "py-site", Domain: "py.example.com", Kind: model.Python, Status: "active"}, {ID: "node-site", Domain: "node.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:3100", Status: "active"}}
	runtimes := []model.NodeRuntime{{SiteID: "node-site", Entrypoint: "server.js", Port: 3100, NodeVersion: "system", Status: "active"}}
	if _, err := host.OperatorDiagnostics(context.Background(), sites, runtimes); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(runner.calls, "\n")
	for _, name := range []string{"php8.4-fpm.service", "wpx-python-py-site.socket", "wpx-python-py-site.service", "wpx-node-node-site.service"} {
		if !strings.Contains(calls, "show "+name+" ") {
			t.Errorf("missing %s", name)
		}
	}
}
