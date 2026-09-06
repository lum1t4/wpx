//go:build linux

package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

func TestWordPressInventoryOnlyTogglesOrdinaryPlugins(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "inventory-site", Domain: "inventory.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	// Drop-ins and must-use plugins are loaded by WordPress itself; network
	// activation also has a wider scope than this site's ordinary plugin toggle.
	// All are inventory items, but none should offer an unsupported toggle.
	plugins := []broker.WordPressPlugin{
		{Name: "active-plugin", Status: "active", Version: "1.2.3", Update: "none"},
		{Name: "inactive-plugin", Status: "inactive", Version: "2.3.4", Update: "none"},
		{Name: "object-cache.php", Status: "dropin", Version: "2.6.0"},
		{Name: "must-use-plugin", Status: "must-use", Version: "1.0.0"},
		{Name: "mustuse-plugin", Status: "mustuse", Version: "1.0.1"},
		{Name: "network-plugin", Status: "active-network", Version: "3.4.5", Update: "none"},
	}
	privileged.run = func(op broker.Operation, in, out any) error {
		if op != broker.OpWordPressInventory {
			return fmt.Errorf("unexpected inventory-page operation: %s", op)
		}
		if got := in.(broker.WordPressPluginsRequest).Site.ID; got != "inventory-site" {
			t.Errorf("inventory requested for %q, want inventory-site", got)
		}
		*out.(*broker.WordPressInventoryResult) = broker.WordPressInventoryResult{
			CoreVersion: "6.8.3",
			Plugins:     plugins,
			Themes:      []broker.WordPressTheme{{Name: "current-theme", Status: "active", Version: "1.7"}},
		}
		return nil
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/inventory-site/wordpress", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if len(privileged.calls) != 1 || privileged.calls[0] != broker.OpWordPressInventory {
		t.Fatalf("inventory-page operations = %v, want one inventory load", privileged.calls)
	}
	if !strings.Contains(body, "WordPress 6.8.3") || !strings.Contains(body, "current-theme") {
		t.Error("mixed plugin inventory prevented the core version or theme from rendering")
	}
	if strings.Contains(body, `role="alert"`) || strings.Contains(body, "inventory could not be loaded") {
		t.Error("valid mixed plugin inventory rendered a loading error")
	}
	forms := regexp.MustCompile(`(?s)<form\b([^>]*)>(.*?)</form>`).FindAllStringSubmatch(body, -1)
	for _, plugin := range plugins {
		t.Run(plugin.Status, func(t *testing.T) {
			if !strings.Contains(body, plugin.Name) || !strings.Contains(body, plugin.Version) {
				t.Errorf("inventory item %q did not render its name and version", plugin.Name)
			}
			wantAction := ""
			switch plugin.Status {
			case "active":
				wantAction = "deactivate"
			case "inactive":
				wantAction = "activate"
			}
			endpoint := "/sites/inventory-site/wordpress/plugins/" + plugin.Name
			found := 0
			for _, form := range forms {
				if navigationAttribute(form[1], "action") != endpoint {
					continue
				}
				found++
				if wantAction == "" {
					t.Errorf("%s plugin %q exposes an unsupported toggle", plugin.Status, plugin.Name)
					continue
				}
				if got := navigationAttribute(form[1], "method"); got != "post" {
					t.Errorf("toggle method = %q, want post", got)
				}
				if got := navigationInputValue(form[2], "action"); got != wantAction {
					t.Errorf("toggle action = %q, want %q", got, wantAction)
				}
				if navigationInputValue(form[2], "csrf_token") == "" {
					t.Error("toggle is missing its request token")
				}
			}
			if wantAction != "" && found != 1 {
				t.Errorf("ordinary plugin %q has %d toggle forms, want 1", plugin.Name, found)
			}
		})
	}
}
