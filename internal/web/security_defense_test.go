//go:build linux

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestSecuritySettingsRequireAssignmentCapabilityAndCSRF(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "team-wp", Domain: "team.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	navigationSite(t, server, owner, model.Site{ID: "private-wp", Domain: "private.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	collaborator, err := server.store.CreateUser(context.Background(), owner, "security-collaborator", "security-test-password", rbac.Collaborator, []string{"team-wp"})
	if err != nil {
		t.Fatal(err)
	}
	customer, err := server.store.CreateUser(context.Background(), owner, "security-customer", "security-test-password", rbac.Customer, []string{"team-wp"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"enabled": {"yes"}, "login_protection": {"yes"}, "xmlrpc_protection": {"yes"}, "sensitive_path_protection": {"yes"}, "burst_404_protection": {"yes"}}
	response := navigationRequest(t, server, customer, http.MethodPost, "/sites/team-wp/security/settings", form)
	requireNavigationStatus(t, response, http.StatusForbidden)
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/private-wp/security/settings", form)
	requireNavigationStatus(t, response, http.StatusForbidden)
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/team-wp/security/install", url.Values{})
	requireNavigationStatus(t, response, http.StatusForbidden)
	if _, err := server.store.EnqueueSecurityInstall(context.Background(), collaborator, "team-wp"); err == nil {
		t.Fatal("site collaborator received server-wide package installation authority")
	}

	token, err := server.store.CreateSession(context.Background(), collaborator.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	form.Set("csrf_token", "wrong")
	request := httptest.NewRequest(http.MethodPost, "/sites/team-wp/security/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
	request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: strings.Repeat("a", 64)})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	requireNavigationStatus(t, response, http.StatusForbidden)
	if len(privileged.calls) != 0 {
		t.Fatalf("unauthorized settings reached broker: %v", privileged.calls)
	}

	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/team-wp/security/settings", form)
	requireNavigationStatus(t, response, http.StatusSeeOther)
	if response.Header().Get("Location") != "/sites/team-wp/security?security=queued" {
		t.Fatalf("redirect=%q", response.Header().Get("Location"))
	}
	settings, err := server.store.SiteSecuritySettings(context.Background(), "team-wp")
	if err != nil || settings.Status != "queued" || !settings.Enabled {
		t.Fatalf("settings=%#v error=%v", settings, err)
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("HTTP settings request performed root work: %v", privileged.calls)
	}
}
