package provision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type fakeOutputRunner struct {
	output []byte
	calls  [][]string
}

type sequenceOutputRunner struct {
	outputs [][]byte
}

func (r *sequenceOutputRunner) Output(context.Context, string, ...string) ([]byte, error) {
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected output call")
	}
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
	plugins, err := host.Plugins(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].Name != "akismet" || plugins[0].UpdateVersion != "5.4" {
		t.Fatalf("unexpected plugin inventory: %#v", plugins)
	}
	if err := host.SetPlugin(context.Background(), site, "akismet", true); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(output.calls[len(output.calls)-1], " ")
	if !strings.Contains(last, "plugin activate akismet") || strings.Contains(last, "sh -c") {
		t.Fatalf("unexpected plugin command: %s", last)
	}
	before := len(output.calls)
	if err := host.SetPlugin(context.Background(), site, "../../escape", false); err == nil {
		t.Fatal("invalid plugin slug was accepted")
	}
	if len(output.calls) != before {
		t.Fatal("invalid slug reached command runner")
	}
}

func TestWordPressInventoryIncludesCorePluginsAndThemes(t *testing.T) {
	host := testHost(t, &recordRunner{})
	host.Output = &sequenceOutputRunner{outputs: [][]byte{
		[]byte(`[{"name":"akismet","status":"active","version":"5.3","update":"available","update_version":"5.4"}]`),
		[]byte(`[{"name":"twentytwentyfive","status":"active","version":"1.0","update":"none","update_version":""}]`),
		[]byte("6.8.1\n"),
		[]byte(`[{"version":"6.8.2"}]`),
	}}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	inventory, err := host.Inventory(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CoreVersion != "6.8.1" || inventory.CoreUpdateVersion != "6.8.2" || len(inventory.Plugins) != 1 || len(inventory.Themes) != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory)
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
			if _, err := host.Plugins(context.Background(), site); err == nil {
				t.Fatalf("unexpected WP-CLI update shape %s was silently accepted", value)
			}
		})
	}
}
