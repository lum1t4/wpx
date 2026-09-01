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
