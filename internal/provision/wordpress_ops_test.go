package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type fakeOutputRunner struct {
	output []byte
	calls  [][]string
}

type sequenceOutputRunner struct {
	outputs [][]byte
	calls   [][]string
}

type deadlineOutputRunner struct {
	remaining time.Duration
	found     bool
}

func (r *deadlineOutputRunner) Output(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	deadline, ok := ctx.Deadline()
	r.found = ok
	if ok {
		r.remaining = time.Until(deadline)
	}
	return nil, nil
}

func (r *sequenceOutputRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected output call")
	}
	r.calls = append(r.calls, append([]string{executable}, args...))
	result := r.outputs[0]
	r.outputs = r.outputs[1:]
	return result, nil
}

func (r *fakeOutputRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{executable}, args...))
	return r.output, nil
}

func TestPluginInventoryAndMutationUseTypedArguments(t *testing.T) {
	host := testHost(t, &recordRunner{})
	output := &fakeOutputRunner{output: []byte(`[{"name":"akismet","status":"inactive","version":"5.3","update":"available","update_version":"5.4"}]`)}
	host.Output = output
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	createWordPressPublicFixture(t, host, site)
	plugins, err := host.Plugins(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].Name != "akismet" || plugins[0].UpdateVersion != "5.4" {
		t.Fatalf("unexpected plugin inventory: %#v", plugins)
	}
	inventoryCall := strings.Join(output.calls[0], " ")
	if !strings.Contains(inventoryCall, "--skip-plugins --skip-themes plugin list") {
		t.Fatalf("plugin inventory loaded site extensions: %s", inventoryCall)
	}
	if err := host.SetPlugin(context.Background(), site, "akismet", true); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(output.calls[len(output.calls)-1], " ")
	if !strings.Contains(last, "plugin activate akismet") || strings.Contains(last, "--skip-plugins") || strings.Contains(last, "sh -c") {
		t.Fatalf("unexpected plugin command: %s", last)
	}
	if err := host.SetPlugin(context.Background(), site, "akismet", false); err != nil {
		t.Fatal(err)
	}
	last = strings.Join(output.calls[len(output.calls)-1], " ")
	if !strings.Contains(last, "--skip-plugins --skip-themes plugin deactivate akismet") || strings.Contains(last, "sh -c") {
		t.Fatalf("plugin recovery loaded site extensions or used a shell: %s", last)
	}
	before := len(output.calls)
	if err := host.SetPlugin(context.Background(), site, "../../escape", false); err == nil {
		t.Fatal("invalid plugin slug was accepted")
	}
	if len(output.calls) != before {
		t.Fatal("invalid slug reached command runner")
	}
}

type bootstrapFailureOutputRunner struct {
	calls [][]string
}

func (r *bootstrapFailureOutputRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	call := append([]string{executable}, args...)
	r.calls = append(r.calls, call)
	joined := strings.Join(call, " ")
	if strings.Contains(joined, "core is-installed") && !strings.Contains(joined, "--skip-plugins") {
		return nil, errors.New("plugin fatal during bootstrap")
	}
	return nil, nil
}

func TestWordPressHealthSeparatesBrokenBootstrapFromCoreAndDatabase(t *testing.T) {
	host := testHost(t, &recordRunner{})
	output := &bootstrapFailureOutputRunner{}
	host.Output = output
	site := model.Site{ID: "broken-site", Domain: "broken.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	createWordPressPublicFixture(t, host, site)

	result := host.Health(context.Background(), site)
	if len(result.Checks) != 3 {
		t.Fatalf("unexpected checks: %+v", result.Checks)
	}
	if result.Checks[0].Name != "WordPress boots with active plugins and theme" || result.Checks[0].Status != "failed" {
		t.Fatalf("bootstrap failure was not isolated: %+v", result.Checks)
	}
	if result.Checks[1].Status != "healthy" || result.Checks[2].Status != "healthy" {
		t.Fatalf("extension failure obscured core or database checks: %+v", result.Checks)
	}
	for _, call := range output.calls[1:] {
		if joined := strings.Join(call, " "); !strings.Contains(joined, "--skip-plugins --skip-themes") {
			t.Fatalf("diagnostic check loaded site extensions: %s", joined)
		}
	}
}

func TestWordPressCommandReceivesBoundedContext(t *testing.T) {
	host := testHost(t, &recordRunner{})
	output := &deadlineOutputRunner{}
	host.Output = output
	site := model.Site{ID: "bounded-site", Domain: "bounded.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	createWordPressPublicFixture(t, host, site)

	if err := host.SetPlugin(context.Background(), site, "akismet", false); err != nil {
		t.Fatal(err)
	}
	if !output.found || output.remaining <= 0 || output.remaining > wordpressCommandTimeout {
		t.Fatalf("WordPress command was not bounded by %s: found=%v remaining=%s", wordpressCommandTimeout, output.found, output.remaining)
	}
}

func TestWordPressInventoryIncludesCorePluginsAndThemes(t *testing.T) {
	host := testHost(t, &recordRunner{})
	output := &sequenceOutputRunner{outputs: [][]byte{
		[]byte(`[{"name":"akismet","status":"active","version":"5.3","update":"available","update_version":"5.4"}]`),
		[]byte(`[{"name":"twentytwentyfive","status":"active","version":"1.0","update":"none","update_version":""}]`),
		[]byte("6.8.1\n"),
		[]byte(`[{"version":"6.8.2"}]`),
	}}
	host.Output = output
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	createWordPressPublicFixture(t, host, site)
	inventory, err := host.Inventory(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CoreVersion != "6.8.1" || inventory.CoreUpdateVersion != "6.8.2" || len(inventory.Plugins) != 1 || len(inventory.Themes) != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
	if inventory.Plugins[0].Status != "active" || inventory.Themes[0].Status != "active" {
		t.Fatalf("skipping extension loading hid persisted activation state: %#v", inventory)
	}
	for _, call := range output.calls {
		if joined := strings.Join(call, " "); !strings.Contains(joined, "--skip-plugins --skip-themes") {
			t.Fatalf("inventory command loaded site extensions: %s", joined)
		}
	}
}

func TestWordPressInventoryAcceptsDropInBooleanUpdateStatus(t *testing.T) {
	host := testHost(t, &recordRunner{})
	host.Output = &sequenceOutputRunner{outputs: [][]byte{
		[]byte(`[
			{"name":"redis-cache","status":"active","version":"2.6.0","update":"none","update_version":""},
			{"name":"object-cache.php","status":"dropin","version":"","update":false,"update_version":""},
			{"name":"site-policy","status":"must-use","version":"1.0","update":false,"update_version":""},
			{"name":"akismet","status":"inactive","version":"5.3","update":"available","update_version":"5.4"}
		]`),
		[]byte(`[{"name":"twentytwentyfive","status":"active","version":"1.0","update":"none","update_version":""}]`),
		[]byte("7.1\n"),
		[]byte(`[]`),
	}}
	site := model.Site{ID: "redis-site", Domain: "redis.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	createWordPressPublicFixture(t, host, site)
	inventory, err := host.Inventory(context.Background(), site)
	if err != nil {
		t.Fatalf("Redis drop-in prevented loading WordPress inventory: %v", err)
	}
	if inventory.CoreVersion != "7.1" || len(inventory.Plugins) != 4 || len(inventory.Themes) != 1 {
		t.Fatalf("drop-in caused incomplete inventory: %+v", inventory)
	}
	for i, want := range []string{"none", "none", "none", "available"} {
		if got := inventory.Plugins[i].Update; got != want {
			t.Errorf("plugin %s update = %q, want %q", inventory.Plugins[i].Name, got, want)
		}
	}
	if dropin := inventory.Plugins[1]; dropin.Name != "object-cache.php" || dropin.Status != "dropin" {
		t.Fatalf("normalization lost drop-in identity: %+v", dropin)
	}
	if inventory.Plugins[3].UpdateVersion != "5.4" {
		t.Fatal("normalization lost the available plugin update version")
	}
}

func TestWordPressThemeInventoryNormalizesBooleanUpdateStatus(t *testing.T) {
	host := testHost(t, &recordRunner{})
	host.Output = &sequenceOutputRunner{outputs: [][]byte{
		[]byte(`[]`),
		[]byte(`[{"name":"custom-theme","status":"active","version":"1.0","update":false,"update_version":""}]`),
		[]byte("7.1\n"),
		[]byte(`[]`),
	}}
	site := model.Site{ID: "theme-site", Domain: "theme.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	createWordPressPublicFixture(t, host, site)
	inventory, err := host.Inventory(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Themes) != 1 || inventory.Themes[0].Update != "none" {
		t.Fatalf("unexpected normalized themes: %+v", inventory.Themes)
	}
}

func TestWordPressPluginInventoryRejectsUnexpectedUpdateTypes(t *testing.T) {
	for _, value := range []string{"true", "42", "{}", "[]"} {
		t.Run(value, func(t *testing.T) {
			host := testHost(t, &recordRunner{})
			host.Output = &fakeOutputRunner{output: []byte(`[{"name":"example","status":"active","update":` + value + `}]`)}
			site := model.Site{ID: "plugin-site", Domain: "plugin.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
			createWordPressPublicFixture(t, host, site)
			if _, err := host.Plugins(context.Background(), site); err == nil || !strings.Contains(err.Error(), "decode WP-CLI plugin inventory") {
				t.Fatalf("unexpected WP-CLI update shape %s was silently accepted", value)
			}
		})
	}
}

func createWordPressPublicFixture(t *testing.T, host *Host, site model.Site) string {
	t.Helper()
	publicDir := filepath.Join(host.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(publicDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
		t.Fatal(err)
	}
	return publicDir
}
