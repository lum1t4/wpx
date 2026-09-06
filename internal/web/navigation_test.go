//go:build linux

package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Navigation tests go through the real session and authorization middleware.
// The privileged side is a recording stub: merely opening an overview must not
// execute WordPress or reveal a tool that the signed-in user cannot operate.
type navigationBroker struct {
	calls []broker.Operation
	run   func(broker.Operation, any, any) error
}

func (b *navigationBroker) Call(_ context.Context, op broker.Operation, _ string, in, out any) error {
	b.calls = append(b.calls, op)
	if b.run != nil {
		return b.run(op, in, out)
	}
	switch op {
	case broker.OpWordPressInventory:
		*out.(*broker.WordPressInventoryResult) = broker.WordPressInventoryResult{CoreVersion: "6.8.3"}
	case broker.OpSiteObservability:
		*out.(*broker.SiteObservabilityResult) = broker.SiteObservabilityResult{}
	case broker.OpFileList:
		*out.(*broker.FileListResult) = broker.FileListResult{}
	default:
		return fmt.Errorf("unexpected operation in navigation test: %s", op)
	}
	return nil
}

func navigationServer(t *testing.T) (*Server, store.User, *navigationBroker) {
	t.Helper()
	server, cancel := testServer(t)
	t.Cleanup(cancel)
	owner, err := server.store.CreateOwner(context.Background(), "test-owner", "navigation-test-password")
	if err != nil {
		t.Fatal(err)
	}
	privileged := &navigationBroker{}
	server.broker = privileged
	return server, owner, privileged
}

func navigationSite(t *testing.T, server *Server, owner store.User, site model.Site) {
	t.Helper()
	ctx := context.Background()
	if _, err := server.store.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found || job.TargetID != site.ID {
		t.Fatalf("claim provisioning fixture: found=%t target=%q error=%v", found, job.TargetID, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
}

func navigationRequest(t *testing.T, server *Server, user store.User, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	token, err := server.store.CreateSession(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	csrf := strings.Repeat("a", 64)
	if form != nil {
		form.Set("csrf_token", csrf)
	}
	request := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
	request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: csrf})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func requireNavigationStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, want, response.Body.String())
	}
	if want == http.StatusOK && !strings.Contains(response.Body.String(), "</html>") {
		t.Fatal("successful page was only partially rendered")
	}
}

func TestSiteSectionPermissions(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "team-site", Domain: "team.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	navigationSite(t, server, owner, model.Site{ID: "private-site", Domain: "private.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	customer, err := server.store.CreateUser(context.Background(), owner, "test-customer", "navigation-test-password", rbac.Customer, []string{"team-site"})
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := server.store.CreateUser(context.Background(), owner, "test-collaborator", "navigation-test-password", rbac.Collaborator, []string{"team-site"})
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"", "/wordpress", "/staging", "/backups", "/security", "/settings", "/files", "/observability", "/dns"} {
		t.Run("owner"+section, func(t *testing.T) {
			response := navigationRequest(t, server, owner, http.MethodGet, "/sites/team-site"+section, nil)
			requireNavigationStatus(t, response, http.StatusOK)
		})
	}
	for _, section := range []string{"", "/backups", "/observability"} {
		t.Run("customer"+section, func(t *testing.T) {
			response := navigationRequest(t, server, customer, http.MethodGet, "/sites/team-site"+section, nil)
			requireNavigationStatus(t, response, http.StatusOK)
			links := navigationLinks(response.Body.String())
			for _, restricted := range []string{"/wordpress", "/staging", "/security", "/settings", "/files", "/dns"} {
				if links["/sites/team-site"+restricted] {
					t.Errorf("customer navigation exposes unavailable tool %s", restricted)
				}
			}
			for _, restricted := range []string{"/sites/new", "/users", "/backups/targets", "/dns/providers"} {
				if links[restricted] {
					t.Errorf("customer navigation exposes server tool %s", restricted)
				}
			}
		})
	}
	for _, path := range []string{"/sites/team-site/wordpress", "/sites/team-site/staging", "/sites/team-site/security", "/sites/team-site/settings", "/sites/team-site/files", "/sites/team-site/dns", "/sites/new", "/sites/private-site", "/sites/private-site/backups"} {
		t.Run("customer denied "+path, func(t *testing.T) {
			requireNavigationStatus(t, navigationRequest(t, server, customer, http.MethodGet, path, nil), http.StatusForbidden)
		})
	}
	for _, section := range []string{"/wordpress", "/staging", "/security", "/backups"} {
		t.Run("collaborator"+section, func(t *testing.T) {
			requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/team-site"+section, nil), http.StatusOK)
		})
	}
	requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/team-site/settings", nil), http.StatusForbidden)
	requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/new", nil), http.StatusForbidden)
}

func TestSiteOverviewDoesNotLoadWordPressInventory(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "wordpress-site", Domain: "wordpress.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	for _, section := range []string{"", "/backups", "/security", "/settings", "/staging"} {
		requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodGet, "/sites/wordpress-site"+section, nil), http.StatusOK)
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("non-WordPress tool pages invoked privileged operations: %v", privileged.calls)
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/wordpress-site/wordpress", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if len(privileged.calls) != 1 || privileged.calls[0] != broker.OpWordPressInventory {
		t.Fatalf("WordPress page operations = %v, want exactly one inventory call", privileged.calls)
	}
	if !strings.Contains(response.Body.String(), "6.8.3") {
		t.Fatal("WordPress page did not render the loaded core version")
	}
}

func TestUnknownRoutesAreNotDashboardAliases(t *testing.T) {
	server, owner, _ := navigationServer(t)
	for _, path := range []string{"/not-a-page", "/sites/missing/unknown-tool", "/account/not-a-page"} {
		t.Run(path, func(t *testing.T) {
			requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodGet, path, nil), http.StatusNotFound)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			requireNavigationStatus(t, response, http.StatusNotFound)
		})
	}
}

func TestSiteListFiltersAndDedicatedCreation(t *testing.T) {
	server, owner, _ := navigationServer(t)
	for _, site := range []model.Site{
		{ID: "alpha-wordpress", Domain: "alpha.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "beta-wordpress", Domain: "beta.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "alpha-static", Domain: "alpha-static.example.com", Kind: model.Static},
	} {
		navigationSite(t, server, owner, site)
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites?kind=wordpress&q=ALPHA", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	links := navigationLinks(response.Body.String())
	if !links["/sites/alpha-wordpress"] || links["/sites/beta-wordpress"] || links["/sites/alpha-static"] {
		t.Fatalf("site filters did not combine case-insensitive query and site kind: %v", links)
	}
	if regexp.MustCompile(`<form\b[^>]*method="post"[^>]*action="/sites"`).MatchString(response.Body.String()) {
		t.Fatal("site list still contains the creation form")
	}
	response = navigationRequest(t, server, owner, http.MethodGet, "/sites/new", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `action="/sites"`) || !strings.Contains(response.Body.String(), `name="domain"`) {
		t.Fatal("dedicated new-site route did not render the site creation form")
	}
}

func TestCreateSiteValidationPreservesFormValues(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{"id": {"ignored--id"}, "domain": {"https://draft.example.com"}, "kind": {"wordpress"}, "php_version": {"8.0"}, "allow_eol": {"yes"}, "wordpress_multisite": {"subdirectories"}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites", form)
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body := response.Body.String()
	for _, name := range []string{"domain", "kind"} {
		if got := navigationInputValue(body, name); got != form.Get(name) {
			t.Errorf("%s after validation = %q, want %q", name, got, form.Get(name))
		}
	}
	for _, name := range []string{"php_version", "wordpress_multisite"} {
		if got := navigationSelectedValue(body, name); got != form.Get(name) {
			t.Errorf("selected %s after validation = %q, want %q", name, got, form.Get(name))
		}
	}
	if !strings.Contains(body, `action="/sites"`) {
		t.Fatal("validation error did not return to an editable creation form")
	}
	sites, err := server.store.ListSites(context.Background())
	if err != nil || len(sites) != 0 {
		t.Fatalf("invalid site was persisted: sites=%v error=%v", sites, err)
	}
}

func TestFileNavigationPreservesDirectoryAndEscapesQueryValues(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "files-site", Domain: "files.example.com", Kind: model.Static})
	directory := "assets & files"
	filename := directory + "/index + draft.php"
	sibling := directory + "/other & final.php"
	child := directory + "/folder + child"
	privileged.run = func(op broker.Operation, in, out any) error {
		switch op {
		case broker.OpFileList:
			if got := in.(broker.FileRequest).Path; got != directory {
				t.Errorf("listed directory = %q, want %q", got, directory)
			}
			*out.(*broker.FileListResult) = broker.FileListResult{Entries: []broker.FileEntry{
				{Name: "other & final.php", Path: sibling},
				{Name: "folder + child", Path: child, IsDir: true},
			}}
		case broker.OpFileRead:
			if got := in.(broker.FileRequest).Path; got != filename {
				t.Errorf("opened file = %q, want %q", got, filename)
			}
			*out.(*broker.FileReadResult) = broker.FileReadResult{Content: "<?php echo 'draft';"}
		default:
			return fmt.Errorf("unexpected file navigation operation %s", op)
		}
		return nil
	}
	query := url.Values{"path": {directory}, "edit": {filename}}
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/files-site/files?"+query.Encode(), nil)
	requireNavigationStatus(t, response, http.StatusOK)
	foundSibling, foundChild := false, false
	for href := range navigationLinks(response.Body.String()) {
		parsed, err := url.Parse(href)
		if err != nil || parsed.Path != "/sites/files-site/files" {
			continue
		}
		if parsed.Query().Get("edit") == sibling {
			foundSibling = true
			if got := parsed.Query().Get("path"); got != directory {
				t.Errorf("sibling link lists %q instead of current directory %q", got, directory)
			}
		}
		if parsed.Query().Get("path") == child {
			foundChild = true
		}
	}
	if !foundSibling || !foundChild {
		t.Fatalf("encoded file links missing: sibling=%t directory=%t", foundSibling, foundChild)
	}
}

func navigationLinks(body string) map[string]bool {
	links := make(map[string]bool)
	for _, match := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		links[html.UnescapeString(match[1])] = true
	}
	return links
}

func navigationInputValue(body, name string) string {
	for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		if navigationAttribute(tag, "name") == name {
			return navigationAttribute(tag, "value")
		}
	}
	return ""
}

func navigationSelectedValue(body, name string) string {
	for _, selectTag := range regexp.MustCompile(`(?s)<select\b([^>]*)>(.*?)</select>`).FindAllStringSubmatch(body, -1) {
		if navigationAttribute(selectTag[1], "name") != name {
			continue
		}
		for _, option := range regexp.MustCompile(`<option\b[^>]*>`).FindAllString(selectTag[2], -1) {
			if regexp.MustCompile(`\sselected(?:\s|=|>)`).MatchString(option) {
				return navigationAttribute(option, "value")
			}
		}
	}
	return ""
}

func navigationAttribute(tag, name string) string {
	match := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `="([^"]*)"`).FindStringSubmatch(tag)
	if len(match) != 2 {
		return ""
	}
	return html.UnescapeString(match[1])
}
