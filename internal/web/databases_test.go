//go:build linux

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestDatabaseCreateReturnsRealQueuedAction(t *testing.T) {
	server, cancel := testServer(t)
	t.Cleanup(cancel)
	ctx := context.Background()
	owner, err := server.store.CreateOwner(ctx, "database-owner", "database-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "database-action-site", Domain: "database-action.example.com", Kind: model.PHP, PHPVersion: "8.4"}
	if _, err := server.store.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	provision, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim site job: found=%t error=%v", found, err)
	}
	if err := server.store.FinishJob(ctx, provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	session, err := server.store.CreateSession(ctx, owner.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	csrf := strings.Repeat("d", 64)
	form := url.Values{"site_id": {site.ID}, "label": {"Action database"}, "csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "/databases", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-WPX-Action", "partial")
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: session})
	request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: csrf})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var action actionResponse
	if err := json.NewDecoder(response.Body).Decode(&action); err != nil {
		t.Fatal(err)
	}
	if action.JobID == "" || action.Status != "queued" || action.RedirectURL != "/databases?created=queued" {
		t.Fatalf("unexpected queued response: %#v", action)
	}
	job, err := server.store.JobForUser(ctx, owner, action.JobID)
	if err != nil || job.Kind != "database.create" || job.TargetID == "" {
		t.Fatalf("queued database job=%#v error=%v", job, err)
	}
}

func TestDatabasePagesUseUnifiedListsAndNativeDialogs(t *testing.T) {
	server, owner, _ := navigationServer(t)
	ctx := context.Background()
	navigationSite(t, server, owner, model.Site{ID: "database-ui-site", Domain: "app.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	navigationSite(t, server, owner, model.Site{ID: "database-ui-wordpress", Domain: "blog.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	database, _, err := server.store.CreateDatabase(ctx, owner, "database-ui-site", "Store data")
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found || job.TargetID != database.ID {
		t.Fatalf("claim database job: job=%#v found=%t err=%v", job, found, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.EnqueueDatabaseAdminInstall(ctx, owner); err != nil {
		t.Fatal(err)
	}
	job, found, err = server.store.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.admin_install" {
		t.Fatalf("claim phpMyAdmin job: job=%#v found=%t err=%v", job, found, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}

	response := navigationRequest(t, server, owner, http.MethodGet, "/databases", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	for _, want := range []string{"Store data", "blog.example.com", `data-database-create-dialog`, `data-database-delete-dialog`, `Open phpMyAdmin`, assetURL("databases.js"), `<noscript>`} {
		if !strings.Contains(body, want) {
			t.Errorf("global database page is missing %q", want)
		}
	}
	if strings.Contains(body, ">Ready<") || strings.Contains(body, "Application databases") || strings.Contains(body, "WordPress databases") {
		t.Fatal("global database page retained the split inventory or Ready strip")
	}

	response = navigationRequest(t, server, owner, http.MethodPost, "/databases", url.Values{"site_id": {"database-ui-site"}, "label": {"x"}})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body = response.Body.String()
	if !strings.Contains(body, `data-open-on-load`) || !strings.Contains(body, `value="database-ui-site" selected`) || !strings.Contains(body, `name="label" value="x"`) {
		t.Fatal("invalid create did not reopen the dialog with nonsecret form state")
	}

	response = navigationRequest(t, server, owner, http.MethodGet, "/sites/database-ui-site/databases", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body = response.Body.String()
	if !strings.Contains(body, "Store data") || strings.Contains(body, "blog.example.com") || !strings.Contains(body, `id="create-site-database"`) {
		t.Fatal("site database list or create dialog is not scoped to its site")
	}
}

func TestDatabaseAdminCookiesExcludePanelSecrets(t *testing.T) {
	got := databaseAdminCookies("wpx_session=panel-secret; __Secure-phpMyAdmin_https=session; wpx_csrf=csrf-secret; __Secure-pma_lang_https=en")
	if got != "__Secure-phpMyAdmin_https=session; __Secure-pma_lang_https=en" {
		t.Fatalf("filtered cookies=%q", got)
	}
}

func TestDatabaseAdminSignonStartsInEnglish(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusSeeOther,
		Header:     http.Header{"Location": {"/phpmyadmin/index.php"}},
		Request:    &http.Request{URL: &url.URL{Path: "/wpx-signon.php"}},
	}
	if err := scopeDatabaseAdminCookies(response); err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Location"); got != "/phpmyadmin/index.php?lang=en" {
		t.Fatalf("sign-on redirect=%q", got)
	}
}

func TestDatabaseAdminResponseCookiesAreSecureAndCannotReplacePanelSession(t *testing.T) {
	response := &http.Response{Header: http.Header{"Set-Cookie": {
		"__Secure-phpMyAdmin_https=session-id; Path=/; HttpOnly; SameSite=Lax",
		"WPXSignon=signon-id; Path=/phpmyadmin/; HttpOnly",
		"wpx_session=attacker-value; Path=/; HttpOnly",
	}}}
	if err := scopeDatabaseAdminCookies(response); err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	if len(cookies) != 2 {
		t.Fatalf("scoped cookies=%v", response.Header.Values("Set-Cookie"))
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.Path != "/phpmyadmin/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie was not securely scoped: %#v", cookie)
		}
		if cookie.Name == "wpx_session" || cookie.Name == "wpx_csrf" {
			t.Fatalf("panel cookie escaped response boundary: %#v", cookie)
		}
	}
}
