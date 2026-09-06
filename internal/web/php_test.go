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
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func TestPHPVersionRequestQueuesDurableChange(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     model.SiteKind
		version  string
		allowEOL string
		role     rbac.Role
	}{
		{"owner WordPress", model.WordPress, "8.5", "", rbac.Owner},
		{"owner PHP with EOL confirmation", model.PHP, "8.0", "yes", rbac.Owner},
		{"administrator PHP", model.PHP, "8.3", "", rbac.Administrator},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			navigationSite(t, server, owner, model.Site{ID: "runtime-site", Domain: "runtime.example.com", Kind: test.kind, PHPVersion: "8.4"})
			actor := owner
			if test.role != rbac.Owner {
				var err error
				actor, err = server.store.CreateUser(context.Background(), owner, "runtime-admin", "runtime-test-password", test.role, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			settings := navigationRequest(t, server, actor, http.MethodGet, "/sites/runtime-site/settings", nil)
			requireNavigationStatus(t, settings, http.StatusOK)
			if !strings.Contains(settings.Body.String(), `action="/sites/runtime-site/php-version"`) {
				t.Fatal("active site settings do not offer PHP version management")
			}
			response := navigationRequest(t, server, actor, http.MethodPost, "/sites/runtime-site/php-version", url.Values{"php_version": {test.version}, "allow_eol": {test.allowEOL}})
			requireNavigationStatus(t, response, http.StatusSeeOther)
			if got := response.Header().Get("Location"); got != "/sites/runtime-site/settings?php=queued" {
				t.Fatalf("PHP change redirect = %q", got)
			}
			site, err := server.store.Site(context.Background(), "runtime-site")
			if err != nil || site.Status != "php_changing" || site.PHPVersion != "8.4" {
				t.Fatalf("queued switch must retain installed version: site=%+v error=%v", site, err)
			}
			jobs := phpWebJobs(t, server, owner)
			if len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].TargetID != site.ID {
				t.Fatalf("expected one durable queued PHP switch: %+v", jobs)
			}
			var change model.PHPVersionChange
			if err := json.Unmarshal([]byte(jobs[0].PayloadJSON), &change); err != nil {
				t.Fatal(err)
			}
			if change.PreviousVersion != "8.4" || change.Version != test.version || change.AllowEOL != (test.allowEOL == "yes") {
				t.Fatalf("queued PHP switch lost form intent: %+v", change)
			}
			if len(privileged.calls) != 0 {
				t.Fatalf("request executed privileged work instead of queuing: %v", privileged.calls)
			}
		})
	}
}

func TestPHPVersionChangeRequiresAdministratorAndCSRF(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "runtime-site", Domain: "runtime.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	for _, role := range []rbac.Role{rbac.Collaborator, rbac.Customer} {
		t.Run(string(role), func(t *testing.T) {
			user, err := server.store.CreateUser(context.Background(), owner, "runtime-"+string(role), "runtime-test-password", role, []string{"runtime-site"})
			if err != nil {
				t.Fatal(err)
			}
			response := navigationRequest(t, server, user, http.MethodPost, "/sites/runtime-site/php-version", url.Values{"php_version": {"8.5"}})
			requireNavigationStatus(t, response, http.StatusForbidden)
		})
	}
	for _, csrf := range []string{"", "wrong"} {
		t.Run("csrf="+csrf, func(t *testing.T) {
			token, err := server.store.CreateSession(context.Background(), owner.ID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/sites/runtime-site/php-version", strings.NewReader(url.Values{"php_version": {"8.5"}, "csrf_token": {csrf}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
			request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: strings.Repeat("a", 64)})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			requireNavigationStatus(t, response, http.StatusForbidden)
		})
	}
	if jobs := phpWebJobs(t, server, owner); len(jobs) != 0 {
		t.Fatalf("unauthorized requests queued PHP jobs: %+v", jobs)
	}
	site, err := server.store.Site(context.Background(), "runtime-site")
	if err != nil || site.Status != "active" || site.PHPVersion != "8.4" {
		t.Fatalf("unauthorized requests changed site: %+v error=%v", site, err)
	}
}

func TestPHPVersionValidationKeepsSettingsAndSnippets(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "runtime-site", Domain: "runtime.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	ctx := context.Background()
	snippets := model.SiteSnippets{Nginx: "client_max_body_size 128M;", PHP: "php_admin_value[memory_limit] = 512M"}
	if _, err := server.store.SetSiteSnippets(ctx, owner, "runtime-site", snippets); err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "site.config_apply" {
		t.Fatalf("claim configuration fixture: found=%t job=%+v error=%v", found, job, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ version, errorFragment string }{{"9.0", "PHP version"}, {"8.0", "end-of-life"}} {
		t.Run(test.version, func(t *testing.T) {
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/runtime-site/php-version", url.Values{"php_version": {test.version}})
			requireNavigationStatus(t, response, http.StatusBadRequest)
			body := response.Body.String()
			if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, test.errorFragment) || !strings.Contains(body, "</html>") {
				t.Fatal("invalid PHP selection did not return complete settings with a clear error")
			}
			if formTextareaValue(body, "nginx") != snippets.Nginx || formTextareaValue(body, "php") != snippets.PHP {
				t.Fatal("PHP selection error dropped the site's advanced configuration")
			}
			if !strings.Contains(body, `action="/sites/runtime-site/php-version"`) {
				t.Fatal("invalid PHP selection did not preserve a retryable version form")
			}
		})
	}
	if jobs := phpWebJobs(t, server, owner); len(jobs) != 0 {
		t.Fatalf("invalid PHP requests queued jobs: %+v", jobs)
	}
}

func TestPHPVersionChangeRejectsNonPHPSites(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "static-site", Domain: "static.example.com", Kind: model.Static})
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/static-site/php-version", url.Values{"php_version": {"8.5"}})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "WordPress or PHP site") {
		t.Fatal("unsupported site type did not explain why PHP cannot be changed")
	}
	if jobs := phpWebJobs(t, server, owner); len(jobs) != 0 {
		t.Fatalf("non-PHP site queued a PHP switch: %+v", jobs)
	}
}

func TestPHPChangingPagesDoNotRunInventoryOrOfferAnotherSwitch(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "runtime-site", Domain: "runtime.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	if _, err := server.store.EnqueuePHPVersionChange(context.Background(), owner, "runtime-site", "8.5", false); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"", "/wordpress", "/settings"} {
		response := navigationRequest(t, server, owner, http.MethodGet, "/sites/runtime-site"+section, nil)
		requireNavigationStatus(t, response, http.StatusOK)
		if strings.Contains(response.Body.String(), `action="/sites/runtime-site/php-version"`) {
			t.Fatalf("%s offers another runtime switch while one is pending", section)
		}
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("pending PHP switch pages invoked broker work: %v", privileged.calls)
	}
}

func phpWebJobs(t *testing.T, server *Server, owner store.User) []store.Job {
	t.Helper()
	summaries, err := server.store.RecentJobsForUser(context.Background(), owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []store.Job
	for _, summary := range summaries {
		if summary.Kind != "site.php_version" {
			continue
		}
		job, err := server.store.Job(context.Background(), summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	return jobs
}
