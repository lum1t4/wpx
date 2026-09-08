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
	for _, section := range []string{"", "/wordpress", "/staging", "/backups", "/databases", "/security", "/settings", "/files", "/observability", "/dns"} {
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
			for _, restricted := range []string{"/wordpress", "/staging", "/security", "/settings", "/files", "/databases", "/dns", "/access", "/cron", "/runtime", "/ftp"} {
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
	for _, path := range []string{"/sites/team-site/wordpress", "/sites/team-site/staging", "/sites/team-site/security", "/sites/team-site/settings", "/sites/team-site/files", "/sites/team-site/databases", "/sites/team-site/dns", "/sites/new", "/sites/private-site", "/sites/private-site/backups", "/phpmyadmin/"} {
		t.Run("customer denied "+path, func(t *testing.T) {
			requireNavigationStatus(t, navigationRequest(t, server, customer, http.MethodGet, path, nil), http.StatusForbidden)
		})
	}
	for _, section := range []string{"/wordpress", "/staging", "/security", "/backups", "/databases"} {
		t.Run("collaborator"+section, func(t *testing.T) {
			requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/team-site"+section, nil), http.StatusOK)
		})
	}
	requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/team-site/settings", nil), http.StatusForbidden)
	requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/sites/new", nil), http.StatusForbidden)
}

func TestSiteAndServerNavigationStaySeparate(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "navigation-site", Domain: "navigation.example.com", Kind: model.WordPress, PHPVersion: "8.4"})

	siteResponse := navigationRequest(t, server, owner, http.MethodGet, "/sites/navigation-site", nil)
	requireNavigationStatus(t, siteResponse, http.StatusOK)
	siteLinks := navigationAsideLinks(t, siteResponse.Body.String())
	if strings.Contains(navigationAside(t, siteResponse.Body.String()), `action="/language"`) {
		t.Error("site sidebar exposes account preferences")
	}
	for _, want := range []string{
		"/sites",
		"/sites/navigation-site",
		"/sites/navigation-site/access",
		"/sites/navigation-site/wordpress/search-replace",
		"/sites/navigation-site/wordpress/debug",
		"/sites/navigation-site/cron",
		"/sites/navigation-site/ftp",
	} {
		if !siteLinks[want] {
			t.Errorf("site sidebar is missing %q", want)
		}
	}
	siteAside := navigationAside(t, siteResponse.Body.String())
	if !strings.Contains(siteAside, `data-site-identity`) || !strings.Contains(siteAside, `title="navigation.example.com"`) {
		t.Error("site sidebar is missing the grouped site identity")
	}
	if strings.Count(siteResponse.Body.String(), `data-site-identity`) != 2 {
		t.Error("grouped site identity is not rendered in both desktop and mobile navigation")
	}
	for _, href := range []string{
		"/sites/navigation-site",
		"/sites/navigation-site/access",
		"/sites/navigation-site/wordpress",
		"/sites/navigation-site/wordpress/search-replace",
		"/sites/navigation-site/wordpress/debug",
		"/sites/navigation-site/staging",
		"/sites/navigation-site/backups",
		"/sites/navigation-site/files",
		"/sites/navigation-site/databases",
		"/sites/navigation-site/dns",
		"/sites/navigation-site/security",
		"/sites/navigation-site/cron",
		"/sites/navigation-site/ftp",
		"/sites/navigation-site/observability",
		"/sites/navigation-site/settings",
	} {
		link := regexp.MustCompile(`(?s)<a href="` + regexp.QuoteMeta(href) + `"[^>]*>(.*?)</a>`).FindStringSubmatch(siteAside)
		if len(link) != 2 || !strings.Contains(link[1], "<svg") {
			t.Errorf("site sidebar link %q is missing its icon", href)
		}
	}
	if siteLinks["/sites/navigation-site/runtime"] {
		t.Error("WordPress site sidebar exposes reverse-proxy runtime settings")
	}
	pending := model.Site{ID: "pending-wordpress", Domain: "pending.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if _, err := server.store.CreateSite(context.Background(), owner, pending); err != nil {
		t.Fatal(err)
	}
	pendingResponse := navigationRequest(t, server, owner, http.MethodGet, "/sites/pending-wordpress", nil)
	requireNavigationStatus(t, pendingResponse, http.StatusOK)
	pendingLinks := navigationAsideLinks(t, pendingResponse.Body.String())
	for _, href := range []string{"/sites/pending-wordpress/wordpress/search-replace", "/sites/pending-wordpress/wordpress/debug"} {
		if pendingLinks[href] {
			t.Errorf("inactive WordPress site sidebar exposes %q", href)
		}
	}
	pendingJob, found, err := server.store.ClaimNextJob(context.Background())
	if err != nil || !found || pendingJob.TargetID != pending.ID {
		t.Fatalf("claim pending WordPress fixture: found=%t target=%q error=%v", found, pendingJob.TargetID, err)
	}
	if err := server.store.FinishJob(context.Background(), pendingJob, "{}", nil); err != nil {
		t.Fatal(err)
	}

	navigationSite(t, server, owner, model.Site{ID: "proxy-site", Domain: "proxy.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:3000"})
	proxyResponse := navigationRequest(t, server, owner, http.MethodGet, "/sites/proxy-site", nil)
	requireNavigationStatus(t, proxyResponse, http.StatusOK)
	if !navigationAsideLinks(t, proxyResponse.Body.String())["/sites/proxy-site/runtime"] {
		t.Error("reverse-proxy site sidebar is missing runtime settings")
	}
	runtimeLink := regexp.MustCompile(`(?s)<a href="/sites/proxy-site/runtime"[^>]*>(.*?)</a>`).FindStringSubmatch(navigationAside(t, proxyResponse.Body.String()))
	if len(runtimeLink) != 2 || !strings.Contains(runtimeLink[1], "<svg") {
		t.Error("reverse-proxy runtime link is missing its icon")
	}
	for _, serverTool := range []string{"/", "/jobs", "/monitoring", "/wordpress", "/databases", "/backups/targets", "/dns/providers", "/hosting", "/alerts", "/users"} {
		if siteLinks[serverTool] {
			t.Errorf("site sidebar exposes server tool %q", serverTool)
		}
	}

	serverResponse := navigationRequest(t, server, owner, http.MethodGet, "/", nil)
	requireNavigationStatus(t, serverResponse, http.StatusOK)
	serverLinks := navigationAsideLinks(t, serverResponse.Body.String())
	if strings.Contains(navigationAside(t, serverResponse.Body.String()), `action="/language"`) {
		t.Error("server sidebar exposes account preferences")
	}
	for _, want := range []string{"/", "/sites", "/jobs", "/monitoring", "/wordpress", "/databases", "/backups/targets", "/dns/providers", "/hosting", "/alerts", "/users"} {
		if !serverLinks[want] {
			t.Errorf("server sidebar is missing %q", want)
		}
	}
	for href := range serverLinks {
		if strings.HasPrefix(href, "/sites/navigation-site/") {
			t.Errorf("server sidebar exposes site tool %q", href)
		}
	}
	for name, body := range map[string]string{"site": siteResponse.Body.String(), "server": serverResponse.Body.String()} {
		shell, _, _ := strings.Cut(body, `<main id="main-content"`)
		requireIntrinsicSVGDimensions(t, name+" shared shell", shell)
		if !strings.Contains(body, `href="/account/security"`) {
			t.Errorf("%s page account menu does not link to account settings", name)
		}
		trigger := regexp.MustCompile(`(?s)<summary[^>]*aria-label="Account settings"[^>]*>(.*?)</summary>`).FindStringSubmatch(body)
		if len(trigger) != 2 {
			t.Errorf("%s page is missing the accessible account trigger", name)
		} else if strings.Contains(trigger[1], owner.Username) {
			t.Errorf("%s page account trigger includes the username", name)
		}
		opening := regexp.MustCompile(`<summary[^>]*aria-label="Account settings"[^>]*>`).FindString(body)
		if !strings.Contains(opening, "rounded-full") {
			t.Errorf("%s page account trigger is not round", name)
		}
	}
}

func requireIntrinsicSVGDimensions(t *testing.T, name, fragment string) {
	t.Helper()
	tags := regexp.MustCompile(`<svg\b[^>]*>`).FindAllString(fragment, -1)
	if len(tags) == 0 {
		t.Fatalf("%s contains no SVG icons", name)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, ` width="`) || !strings.Contains(tag, ` height="`) {
			t.Errorf("%s contains an SVG without intrinsic dimensions: %s", name, tag)
		}
	}
}

func TestAccountSettingsContainPreferences(t *testing.T) {
	server, owner, _ := navigationServer(t)
	response := navigationRequest(t, server, owner, http.MethodGet, "/account/security", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()

	for _, want := range []string{
		`<meta name="color-scheme" content="light dark">`,
		`<script src="/assets/theme.js?v=`,
		`action="/language"`,
		`name="next" value="/account/security"`,
		`name="appearance" value="system" data-theme-choice checked`,
		`name="appearance" value="light" data-theme-choice`,
		`name="appearance" value="dark" data-theme-choice`,
		`data-theme-status role="status" aria-live="polite"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("account settings are missing %q", want)
		}
	}
	if strings.Index(body, `/assets/theme.js`) > strings.Index(body, `/assets/app.css`) {
		t.Error("theme script must load before the stylesheet")
	}
	if strings.Contains(navigationAside(t, body), `href="/account/security"`) {
		t.Error("account settings are redundantly linked in the sidebar")
	}
}

func TestSiteDatabaseToolsAreScopedToTheSite(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "team-site", Domain: "team.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	navigationSite(t, server, owner, model.Site{ID: "private-site", Domain: "private.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	collaborator, err := server.store.CreateUser(context.Background(), owner, "database-developer", "navigation-test-password", rbac.Collaborator, []string{"team-site"})
	if err != nil {
		t.Fatal(err)
	}
	teamDatabase, _, err := server.store.CreateDatabase(context.Background(), owner, "team-site", "Team application")
	if err != nil {
		t.Fatal(err)
	}
	privateDatabase, _, err := server.store.CreateDatabase(context.Background(), owner, "private-site", "Private application")
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(context.Background())
	if err != nil || !found || job.TargetID != teamDatabase.ID {
		t.Fatalf("claim team database job: job=%#v found=%v err=%v", job, found, err)
	}
	if err := server.store.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	response := navigationRequest(t, server, collaborator, http.MethodGet, "/sites/team-site/databases", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), "Team application") || strings.Contains(response.Body.String(), "Private application") {
		t.Fatal("site database page did not keep its database list scoped")
	}
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/team-site/databases/"+teamDatabase.ID+"/reveal", url.Values{})
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if !strings.Contains(body, `id="site-database-credentials-heading"`) ||
		!strings.Contains(body, "Credentials · Team application") ||
		!strings.Contains(body, teamDatabase.Name) ||
		!strings.Contains(body, teamDatabase.Username) ||
		!regexp.MustCompile(`aria-label="Database password"[^>]*value="[^"]+"`).MatchString(body) {
		t.Fatal("assigned collaborator could not reveal site database credentials")
	}
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/team-site/databases", url.Values{"label": {"Second application"}})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/sites/team-site/databases?database=queued" {
		t.Fatalf("site database creation returned %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/team-site/databases/"+privateDatabase.ID+"/reveal", url.Values{})
	requireNavigationStatus(t, response, http.StatusNotFound)
	response = navigationRequest(t, server, collaborator, http.MethodPost, "/sites/private-site/databases/"+teamDatabase.ID+"/reveal", url.Values{})
	requireNavigationStatus(t, response, http.StatusForbidden)
	requireNavigationStatus(t, navigationRequest(t, server, collaborator, http.MethodGet, "/databases", nil), http.StatusForbidden)
}

func TestSiteOverviewDoesNotLoadWordPressInventory(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "wordpress-site", Domain: "wordpress.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	for _, section := range []string{"", "/backups", "/security", "/settings", "/staging"} {
		requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodGet, "/sites/wordpress-site"+section, nil), http.StatusOK)
	}
	for _, operation := range privileged.calls {
		if operation == broker.OpWordPressInventory {
			t.Fatalf("non-WordPress tool page invoked WordPress inventory: %v", privileged.calls)
		}
	}
	before := len(privileged.calls)
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/wordpress-site/wordpress", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if len(privileged.calls) != before+1 || privileged.calls[before] != broker.OpWordPressInventory {
		t.Fatalf("WordPress page operations = %v, want one new inventory call", privileged.calls)
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

func navigationAsideLinks(t *testing.T, body string) map[string]bool {
	t.Helper()
	return navigationLinks(navigationAside(t, body))
}

func navigationAside(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<aside\b.*?</aside>`).FindString(body)
	if match == "" {
		t.Fatal("page has no desktop sidebar")
	}
	return match
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
