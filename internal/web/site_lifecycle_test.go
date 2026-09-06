//go:build linux

package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func TestDomainChangeHTTPQueuesWithoutChangingCurrentIdentity(t *testing.T) {
	for _, role := range []rbac.Role{rbac.Owner, rbac.Administrator} {
		t.Run(string(role), func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			site := lifecycleSite()
			navigationSite(t, server, owner, site)
			actor := lifecycleUser(t, server, owner, role, site.ID)
			response := navigationRequest(t, server, actor, http.MethodPost, "/sites/"+site.ID+"/domain", url.Values{"domain": {" New.Example.com. "}})
			requireNavigationStatus(t, response, http.StatusSeeOther)
			if got := response.Header().Get("Location"); got != "/sites/"+site.ID+"/settings?domain=queued" {
				t.Fatalf("domain change redirect = %q", got)
			}
			current := lifecycleRequireStatus(t, server, site.ID, "domain_changing")
			if current.Domain != site.Domain || current.ID != site.ID {
				t.Fatalf("queued request prematurely changed identity: %+v", current)
			}
			jobs := lifecycleJobs(t, server, owner, "site.domain_change")
			if len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].TargetID != site.ID {
				t.Fatalf("expected one durable domain change: %+v", jobs)
			}
			var change model.DomainChange
			if err := json.Unmarshal([]byte(jobs[0].PayloadJSON), &change); err != nil {
				t.Fatal(err)
			}
			if change.PreviousDomain != site.Domain || change.Domain != "new.example.com" {
				t.Fatalf("queued domain intent = %+v", change)
			}
			if len(privileged.calls) != 0 {
				t.Fatalf("HTTP enqueue invoked privileged operations: %v", privileged.calls)
			}
		})
	}
}

func TestDomainChangeValidationRetainsInputAndSettings(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	site := lifecycleSite()
	navigationSite(t, server, owner, site)
	const invalidDomain = "https://new.example.com/path?draft=1&review=2"
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/domain", url.Values{"domain": {invalidDomain}})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body := response.Body.String()
	if got := navigationInputValue(body, "domain"); got != invalidDomain {
		t.Fatalf("rejected domain input = %q, want %q", got, invalidDomain)
	}
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `action="/sites/`+site.ID+`/domain"`) || !strings.Contains(body, `name="nginx"`) || !strings.Contains(body, "</html>") {
		t.Fatal("domain validation lost the error, retry form, or advanced settings")
	}
	lifecycleRequireStatus(t, server, site.ID, "active")
	if jobs := lifecycleJobs(t, server, owner, "site.domain_change"); len(jobs) != 0 || len(privileged.calls) != 0 {
		t.Fatalf("invalid domain caused work: jobs=%v broker=%v", jobs, privileged.calls)
	}
}

func TestLifecycleAuthorizationAndCSRF(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	site := lifecycleSite()
	navigationSite(t, server, owner, site)
	for _, role := range []rbac.Role{rbac.Administrator, rbac.Collaborator, rbac.Customer} {
		actor := lifecycleUser(t, server, owner, role, site.ID)
		if role == rbac.Administrator {
			settings := navigationRequest(t, server, actor, http.MethodGet, "/sites/"+site.ID+"/settings", nil)
			requireNavigationStatus(t, settings, http.StatusOK)
			if strings.Contains(settings.Body.String(), `action="/sites/`+site.ID+`/delete"`) || strings.Contains(settings.Body.String(), "Delete site permanently") {
				t.Fatal("administrator settings expose owner-only deletion")
			}
		}
		for _, action := range []string{"domain", "domain/retry", "delete"} {
			if role == rbac.Administrator && action != "delete" {
				continue
			}
			t.Run(string(role)+"/"+action, func(t *testing.T) {
				response := navigationRequest(t, server, actor, http.MethodPost, "/sites/"+site.ID+"/"+action, lifecycleForm(site))
				requireNavigationStatus(t, response, http.StatusForbidden)
			})
		}
	}
	for _, action := range []string{"domain", "domain/retry", "delete"} {
		t.Run("csrf/"+action, func(t *testing.T) {
			token, err := server.store.CreateSession(context.Background(), owner.ID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			form := lifecycleForm(site)
			form.Set("csrf_token", "incorrect")
			request := httptest.NewRequest(http.MethodPost, "/sites/"+site.ID+"/"+action, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
			request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: strings.Repeat("a", 64)})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			requireNavigationStatus(t, response, http.StatusForbidden)
		})
	}
	lifecycleRequireStatus(t, server, site.ID, "active")
	if len(lifecycleJobs(t, server, owner, "site.domain_change"))+len(lifecycleJobs(t, server, owner, "site.delete")) != 0 || len(privileged.calls) != 0 {
		t.Fatal("unauthorized lifecycle requests changed state or called the host")
	}
}

func TestDeleteSiteRequiresDomainAndAcknowledgment(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	site := lifecycleSite()
	navigationSite(t, server, owner, site)
	for _, test := range []struct{ name, confirmation, acknowledgment string }{
		{"missing domain", "", "yes"},
		{"different domain", "other.example.com", "yes"},
		{"missing acknowledgment", site.Domain, ""},
		{"invalid acknowledgment", site.Domain, "on"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/delete", url.Values{"confirmation": {test.confirmation}, "acknowledge_delete": {test.acknowledgment}})
			requireNavigationStatus(t, response, http.StatusBadRequest)
			body := response.Body.String()
			formBody := lifecycleRenderedForm(t, body, "/sites/"+site.ID+"/delete")
			if navigationInputValue(formBody, "confirmation") != test.confirmation || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "</html>") {
				t.Fatal("delete validation lost the typed confirmation or retry form")
			}
			lifecycleRequireStatus(t, server, site.ID, "active")
		})
	}
	if jobs := lifecycleJobs(t, server, owner, "site.delete"); len(jobs) != 0 || len(privileged.calls) != 0 {
		t.Fatalf("unconfirmed deletion caused work: jobs=%v broker=%v", jobs, privileged.calls)
	}
}

func TestOwnerCanQueueDeleteForActiveDisabledAndFailedSites(t *testing.T) {
	for _, status := range []string{"active", "disabled", "failed"} {
		t.Run(status, func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			site := lifecycleSite()
			if status == "failed" {
				if _, err := server.store.CreateSite(context.Background(), owner, site); err != nil {
					t.Fatal(err)
				}
				lifecycleFinishNext(t, server, "site.provision", errors.New("provisioning failed"))
			} else {
				navigationSite(t, server, owner, site)
				if status == "disabled" {
					if _, err := server.store.EnqueueSiteDisable(context.Background(), owner, site.ID); err != nil {
						t.Fatal(err)
					}
					lifecycleFinishNext(t, server, "site.disable", nil)
				}
			}
			lifecycleRequireStatus(t, server, site.ID, status)
			settings := navigationRequest(t, server, owner, http.MethodGet, "/sites/"+site.ID+"/settings", nil)
			requireNavigationStatus(t, settings, http.StatusOK)
			if !strings.Contains(settings.Body.String(), `action="/sites/`+site.ID+`/delete"`) {
				t.Fatalf("owner cannot find deletion for a %s site", status)
			}
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/delete", lifecycleForm(site))
			requireNavigationStatus(t, response, http.StatusSeeOther)
			if got := response.Header().Get("Location"); got != "/jobs?delete=queued" {
				t.Fatalf("delete redirect = %q", got)
			}
			lifecycleRequireStatus(t, server, site.ID, "deleting")
			jobs := lifecycleJobs(t, server, owner, "site.delete")
			if len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].TargetID != site.ID {
				t.Fatalf("expected one durable deletion: %+v", jobs)
			}
			if len(privileged.calls) != 0 {
				t.Fatalf("delete request performed host work: %v", privileged.calls)
			}
		})
	}
}

func TestFailedLifecycleRetriesReuseOriginalOperation(t *testing.T) {
	for _, action := range []string{"domain", "delete"} {
		t.Run(action, func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			site := lifecycleSite()
			navigationSite(t, server, owner, site)
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/"+action, lifecycleForm(site))
			requireNavigationStatus(t, response, http.StatusSeeOther)
			kind, failedStatus, pendingStatus, retry := "site.domain_change", "domain_change_failed", "domain_changing", "domain/retry"
			if action == "delete" {
				kind, failedStatus, pendingStatus, retry = "site.delete", "delete_failed", "deleting", "delete"
			}
			original := lifecycleFinishNext(t, server, kind, errors.New("host recovery required"))
			lifecycleRequireStatus(t, server, site.ID, failedStatus)
			settings := navigationRequest(t, server, owner, http.MethodGet, "/sites/"+site.ID+"/settings", nil)
			requireNavigationStatus(t, settings, http.StatusOK)
			if !strings.Contains(settings.Body.String(), `action="/sites/`+site.ID+`/`+retry+`"`) {
				t.Fatal("failed operation does not expose its explicit recovery action")
			}
			form := lifecycleForm(site)
			form.Set("domain", "do-not-switch-again.example.com")
			response = navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/"+retry, form)
			requireNavigationStatus(t, response, http.StatusSeeOther)
			lifecycleRequireStatus(t, server, site.ID, pendingStatus)
			jobs := lifecycleJobs(t, server, owner, kind)
			if len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].ID != original.ID || jobs[0].IdempotencyKey != original.IdempotencyKey || jobs[0].PayloadJSON != original.PayloadJSON {
				t.Fatalf("retry replaced the original operation: original=%+v jobs=%+v", original, jobs)
			}
			if len(privileged.calls) != 0 {
				t.Fatalf("recovery request performed host work: %v", privileged.calls)
			}
		})
	}
}

func TestDeleteSiteWithStagingChildrenIsBlocked(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	site := lifecycleSite()
	navigationSite(t, server, owner, site)
	if _, _, err := server.store.CreateStaging(context.Background(), owner, site.ID, "staging-child", "staging.example.com"); err != nil {
		t.Fatal(err)
	}
	// A failed clone still owns a child row. Finish its job so this assertion
	// exercises the child check rather than an unrelated pending-job guard.
	lifecycleFinishNext(t, server, "wordpress.staging_create", errors.New("staging provisioning failed"))
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/delete", lifecycleForm(site))
	requireNavigationStatus(t, response, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "staging copies first") {
		t.Fatal("parent deletion did not explain the staging-child blocker")
	}
	lifecycleRequireStatus(t, server, site.ID, "active")
	lifecycleRequireStatus(t, server, "staging-child", "failed")
	if len(lifecycleJobs(t, server, owner, "site.delete")) != 0 || len(privileged.calls) != 0 {
		t.Fatal("blocked parent deletion caused queued or privileged work")
	}
}

func TestCriticalSiteLifecycleStatesBlockMutationsButKeepSettingsReadable(t *testing.T) {
	for _, status := range []string{"domain_changing", "domain_change_failed", "deleting", "delete_failed"} {
		t.Run(status, func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			site := lifecycleSite()
			navigationSite(t, server, owner, site)
			kind := "site.domain_change"
			var err error
			if strings.HasPrefix(status, "domain_") {
				_, err = server.store.EnqueueDomainChange(context.Background(), owner, site.ID, "changed.example.com")
			} else {
				kind = "site.delete"
				_, err = server.store.EnqueueSiteDelete(context.Background(), owner, site.ID, site.Domain)
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(status, "failed") {
				lifecycleFinishNext(t, server, kind, errors.New("manual recovery required"))
			}
			lifecycleRequireStatus(t, server, site.ID, status)
			mutations := []struct {
				path string
				form url.Values
			}{
				{"files", url.Values{"path": {"index.php"}, "content": {"<?php echo 'changed';"}}},
				{"wordpress/plugins/akismet", url.Values{"action": {"deactivate"}}},
				{"wordpress/login", url.Values{}},
				{"domain", url.Values{"domain": {"unrelated.example.com"}}},
			}
			// Only the failed operation's own recovery endpoint may bypass the
			// reservation. The other destructive route must remain unavailable.
			if status != "delete_failed" {
				mutations = append(mutations, struct {
					path string
					form url.Values
				}{"delete", lifecycleForm(site)})
			}
			if status != "domain_change_failed" {
				mutations = append(mutations, struct {
					path string
					form url.Values
				}{"domain/retry", lifecycleForm(site)})
			}
			for _, mutation := range mutations {
				response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+site.ID+"/"+mutation.path, mutation.form)
				if response.Code != http.StatusConflict && response.Code != http.StatusForbidden {
					t.Errorf("%s mutation status = %d, want conflict or forbidden: %s", mutation.path, response.Code, response.Body.String())
				}
			}
			settings := navigationRequest(t, server, owner, http.MethodGet, "/sites/"+site.ID+"/settings", nil)
			requireNavigationStatus(t, settings, http.StatusOK)
			lifecycleRequireStatus(t, server, site.ID, status)
			if len(privileged.calls) != 0 {
				t.Fatalf("reserved site mutations invoked the host: %v", privileged.calls)
			}
		})
	}
}

func lifecycleSite() model.Site {
	return model.Site{ID: "lifecycle-site", Domain: "lifecycle.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
}

func lifecycleForm(site model.Site) url.Values {
	return url.Values{"domain": {"changed.example.com"}, "confirmation": {site.Domain}, "acknowledge_delete": {"yes"}}
}

func lifecycleRenderedForm(t *testing.T, body, action string) string {
	t.Helper()
	for _, form := range regexp.MustCompile(`(?s)<form\b([^>]*)>(.*?)</form>`).FindAllStringSubmatch(body, -1) {
		if navigationAttribute(form[1], "action") == action {
			return form[2]
		}
	}
	t.Fatalf("missing form for %s", action)
	return ""
}

func lifecycleUser(t *testing.T, server *Server, owner store.User, role rbac.Role, siteID string) store.User {
	t.Helper()
	if role == rbac.Owner {
		return owner
	}
	user, err := server.store.CreateUser(context.Background(), owner, "lifecycle-"+string(role), "lifecycle-test-password", role, []string{siteID})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func lifecycleRequireStatus(t *testing.T, server *Server, siteID, status string) model.Site {
	t.Helper()
	site, err := server.store.Site(context.Background(), siteID)
	if err != nil || site.Status != status {
		t.Fatalf("site %s status = %q, want %q: %v", siteID, site.Status, status, err)
	}
	return site
}

func lifecycleFinishNext(t *testing.T, server *Server, kind string, operationErr error) store.Job {
	t.Helper()
	job, found, err := server.store.ClaimNextJob(context.Background())
	if err != nil || !found || job.Kind != kind {
		t.Fatalf("claim %s fixture: found=%t job=%+v error=%v", kind, found, job, err)
	}
	if err := server.store.FinishJob(context.Background(), job, "{}", operationErr); err != nil {
		t.Fatal(err)
	}
	return job
}

func lifecycleJobs(t *testing.T, server *Server, owner store.User, kind string) []store.Job {
	t.Helper()
	summaries, err := server.store.RecentJobsForUser(context.Background(), owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []store.Job
	for _, summary := range summaries {
		if summary.Kind != kind {
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
