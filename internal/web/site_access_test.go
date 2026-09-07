//go:build linux

package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestSiteAccessPageQueuesDesiredSettingsWithoutEchoingPassword(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static})
	page := navigationRequest(t, server, owner, http.MethodGet, "/sites/access-site/access", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	if !strings.Contains(page.Body.String(), "Allow Cloudflare traffic only") {
		t.Fatal("site access controls were not rendered")
	}

	password := "a-private-access-password"
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/access-site/access", url.Values{
		"basic_auth_enabled": {"yes"},
		"username":           {"visitor"},
		"password":           {password},
		"cloudflare_only":    {"yes"},
	})
	requireNavigationStatus(t, response, http.StatusSeeOther)
	if strings.Contains(response.Body.String(), password) {
		t.Fatal("password was echoed in the response")
	}
	settings, err := server.store.SiteAccess(context.Background(), "access-site")
	if err != nil || settings.Status != "pending" || !settings.BasicAuthEnabled || !settings.CloudflareOnly || settings.Username != "visitor" {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
}

func TestSiteAccessValidationPreservesNonSecretFields(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static})
	password := "short"
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/access-site/access", url.Values{
		"basic_auth_enabled": {"yes"},
		"username":           {"visitor"},
		"password":           {password},
		"cloudflare_only":    {"yes"},
	})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body := response.Body.String()
	if !strings.Contains(body, `value="visitor"`) || !strings.Contains(body, `name="cloudflare_only" value="yes" checked`) {
		t.Fatal("validation response lost non-secret access choices")
	}
	if strings.Contains(body, password) {
		t.Fatal("validation response echoed the password")
	}
}

func TestSiteAccessRoutesDenyAssignedNonAdministrators(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static})
	navigationSite(t, server, owner, model.Site{ID: "private-site", Domain: "private.example.com", Kind: model.Static})
	for _, role := range []rbac.Role{rbac.Collaborator, rbac.Customer} {
		user, err := server.store.CreateUser(context.Background(), owner, "access-"+string(role), "navigation-test-password", role, []string{"access-site"})
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"access-site", "private-site"} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				response := navigationRequest(t, server, user, method, "/sites/"+target+"/access", url.Values{"cloudflare_only": {"yes"}})
				requireNavigationStatus(t, response, http.StatusForbidden)
			}
		}
	}
}

func TestSiteAccessPostRejectsInvalidCSRFWithoutMutation(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static})
	before, err := server.store.RecentJobsForUser(context.Background(), owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/access-site/access", nil)
	requireNavigationStatus(t, response, http.StatusForbidden)
	after, err := server.store.RecentJobsForUser(context.Background(), owner, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("invalid CSRF mutated jobs: before=%d after=%d err=%v", len(before), len(after), err)
	}
	settings, err := server.store.SiteAccess(context.Background(), "access-site")
	if err != nil || settings.BasicAuthEnabled || settings.CloudflareOnly || settings.Status != "active" {
		t.Fatalf("invalid CSRF mutated settings: settings=%#v err=%v", settings, err)
	}
}
