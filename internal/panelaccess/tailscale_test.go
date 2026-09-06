package panelaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailscaleRefusesToReplaceUnrelatedConfiguration(t *testing.T) {
	for _, status := range []string{
		`{"TCP":{"443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`,
		`{"TCP":{"443":{"TCPForward":"localhost:2222"}}}`,
		`{"AllowFunnel":{"host.ts.net:443":true}}`,
		`{"Foreground":{"session":{"TCP":{"443":{"HTTPS":true}}}}}`,
		`{"TCP":{"443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/docs":{"Path":"/srv/docs"}}}}}`,
		`invalid JSON`,
	} {
		t.Run(status, func(t *testing.T) {
			options, runner, _ := accessOptions(t)
			options.Mode, runner.status = "tailscale", status
			previous := existingProxy(t, options, true)
			if _, err := Configure(context.Background(), options); err == nil {
				t.Fatal("expected conflicting Tailscale configuration to be refused")
			}
			assertProxy(t, options, previous, true)
			if len(runner.commands) != 1 || runner.commands[0] != "/usr/bin/tailscale serve status --json" {
				t.Fatalf("configuration changed after failed preflight: %v", runner.commands)
			}
		})
	}
}

func TestTailscalePreservesOtherPorts(t *testing.T) {
	options, runner, _ := accessOptions(t)
	options.Mode = "tailscale"
	runner.status = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"host.ts.net:8443":{"Handlers":{"/":{"Path":"/srv/docs"}}}},"AllowFunnel":{"host.ts.net:8443":true}}`
	if _, err := Configure(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if strings.Contains(command, "reset") || strings.Contains(command, "8443") || strings.HasSuffix(command, " off") {
			t.Fatalf("unrelated Tailscale route changed: %s", command)
		}
	}
}

func TestTailscaleExistingPanelRouteIsNotRewritten(t *testing.T) {
	options, runner, _ := accessOptions(t)
	options.Mode = "tailscale"
	runner.status = `{"TCP":{"443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/":{"Proxy":"https+insecure://127.0.0.1:9443"}}}}}`
	if _, err := Configure(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if strings.Contains(command, "tailscale serve") && command != "/usr/bin/tailscale serve status --json" {
			t.Fatalf("existing route was rewritten: %s", command)
		}
	}
}

func TestTailscaleRouteIsRemovedWhenListenerChangeFails(t *testing.T) {
	options, runner, _ := accessOptions(t)
	options.Mode = "tailscale"
	previous := existingProxy(t, options, true)
	failed := false
	runner.fail = func(command string, _ int) error {
		if command == "/usr/bin/systemctl restart wpx.service" && !failed {
			failed = true
			return errors.New("restart failure")
		}
		return nil
	}
	if _, err := Configure(context.Background(), options); err == nil {
		t.Fatal("expected listener change failure")
	}
	assertProxy(t, options, previous, true)
	commands := strings.Join(runner.commands, "\n")
	if !strings.Contains(commands, "/usr/bin/tailscale serve --bg --yes --https=443 --set-path=/ off") || strings.Contains(commands, "reset") {
		t.Fatalf("new route was not removed precisely: %s", commands)
	}
}

func mockInstalledTailscale(t *testing.T, options *Options) {
	t.Helper()
	options.TailscalePath = filepath.Join(t.TempDir(), "tailscale")
	if err := os.WriteFile(options.TailscalePath, nil, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestLeavingTailscaleRemovesOnlyPanelRootHandler(t *testing.T) {
	for _, mode := range []string{"local", "public", "domain"} {
		t.Run(mode, func(t *testing.T) {
			options, runner, _ := accessOptions(t)
			options.Mode = mode
			mockInstalledTailscale(t, &options)
			runner.status = `{"TCP":{"443":{"HTTPS":true},"8443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/":{"Proxy":"https+insecure://127.0.0.1:9443"},"/docs":{"Path":"/srv/docs"}}},"host.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://localhost:3000"}}}}}`
			if _, err := Configure(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			commands := strings.Join(runner.commands, "\n")
			if !strings.Contains(commands, options.TailscalePath+" serve --bg --yes --https=443 --set-path=/ off") || strings.Contains(commands, "reset") || strings.Contains(commands, "8443") {
				t.Fatalf("panel root handler was not removed precisely: %s", commands)
			}
		})
	}
}

func TestLeavingTailscalePreservesUnrelatedRootHandler(t *testing.T) {
	options, runner, _ := accessOptions(t)
	options.Mode = "local"
	mockInstalledTailscale(t, &options)
	runner.status = `{"TCP":{"443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/":{"Proxy":"http://localhost:3000"}}}}}`
	if _, err := Configure(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if strings.HasPrefix(command, options.TailscalePath) && command != options.TailscalePath+" serve status --json" {
			t.Fatalf("unrelated Tailscale root changed: %s", command)
		}
	}
}

func TestFailedTransitionRestoresPreviousTailscalePanelRoute(t *testing.T) {
	options, runner, _ := accessOptions(t)
	options.Mode = "local"
	mockInstalledTailscale(t, &options)
	runner.status = `{"TCP":{"443":{"HTTPS":true}},"Web":{"host.ts.net:443":{"Handlers":{"/":{"Proxy":"https+insecure://127.0.0.1:9443"}}}}}`
	failed := false
	runner.fail = func(command string, _ int) error {
		if command == "/usr/bin/systemctl restart wpx.service" && !failed {
			failed = true
			return errors.New("restart failure")
		}
		return nil
	}
	if _, err := Configure(context.Background(), options); err == nil {
		t.Fatal("expected failed transition")
	}
	commands := strings.Join(runner.commands, "\n")
	if !strings.Contains(commands, options.TailscalePath+" serve --bg --yes --https=443 --set-path=/ off") || !strings.Contains(commands, options.TailscalePath+" serve --bg --yes --https=443 "+tailscaleTarget) {
		t.Fatalf("original Tailscale route was not restored: %s", commands)
	}
}
