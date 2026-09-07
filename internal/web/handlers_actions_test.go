//go:build linux

package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestQueuedActionReportsOnlyConfirmedOutcomes(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "quick-site", Domain: "quick.example.com", Kind: model.Static})

	for _, test := range []struct {
		name       string
		finishErr  error
		wantStatus string
		wantCode   int
		wantText   string
	}{
		{name: "completed", wantStatus: "succeeded", wantCode: http.StatusOK, wantText: "Configuration completed."},
		{name: "failed", finishErr: errors.New("nginx rejected the configuration"), wantStatus: "failed", wantCode: http.StatusUnprocessableEntity, wantText: "nginx rejected the configuration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobID, err := server.store.SetSiteSnippets(context.Background(), owner, "quick-site", model.SiteSnippets{Nginx: "client_max_body_size 32M;"})
			if err != nil {
				t.Fatal(err)
			}
			job, found, err := server.store.ClaimNextJob(context.Background())
			if err != nil || !found || job.ID != jobID {
				t.Fatalf("claim job: found=%v job=%+v err=%v", found, job, err)
			}
			if err := server.store.FinishJob(context.Background(), job, "{}", test.finishErr); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/sites/quick-site/expert-config", nil)
			request.Header.Set("X-WPX-Action", "partial")
			response := httptest.NewRecorder()
			server.respondQueuedAction(response, request, owner, jobID, "/sites/quick-site/settings", "Configuration")
			if response.Code != test.wantCode {
				t.Fatalf("status code = %d body=%s", response.Code, response.Body.String())
			}
			var payload actionResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Status != test.wantStatus || payload.Message != test.wantText || payload.RedirectURL != "/sites/quick-site/settings" {
				t.Fatalf("payload = %+v", payload)
			}
		})
	}
}

func TestActionStatusAuthorizationDoesNotTrustJobID(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "private-site", Domain: "private.example.com", Kind: model.Static})
	jobID, err := server.store.SetSiteSnippets(context.Background(), owner, "private-site", model.SiteSnippets{})
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := server.store.CreateUser(context.Background(), owner, "quick-collaborator", "a-secure-test-password", rbac.Collaborator, nil)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/actions/jobs/"+jobID+"?return=https://attacker.example", nil)
	request.SetPathValue("id", jobID)
	response := httptest.NewRecorder()
	server.actionJob(response, request, collaborator)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unassigned user read job: status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/actions/jobs/"+jobID+"?return=https://attacker.example", nil)
	request.SetPathValue("id", jobID)
	response = httptest.NewRecorder()
	server.actionJob(response, request, owner)
	if response.Code != http.StatusAccepted {
		t.Fatalf("owner status=%d body=%s", response.Code, response.Body.String())
	}
	var payload actionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RedirectURL != "/jobs" {
		t.Fatalf("unsafe return URL accepted: %+v", payload)
	}
}

func TestQueuedActionKeepsNoScriptRedirect(t *testing.T) {
	server, owner, _ := navigationServer(t)
	request := httptest.NewRequest(http.MethodPost, "/action", nil)
	response := httptest.NewRecorder()
	server.respondQueuedAction(response, request, owner, "not-read-for-redirect", "/jobs?queued=yes", "Action")
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/jobs?queued=yes" {
		t.Fatalf("redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestActionPollRequiresSession(t *testing.T) {
	server, _, _ := navigationServer(t)
	request := httptest.NewRequest(http.MethodGet, "/actions/jobs/unknown", nil)
	request.SetPathValue("id", "unknown")
	response := httptest.NewRecorder()
	server.requireSession(server.actionJob)(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated poll = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestSafeLocalReturnURLRejectsBrowserNormalizationEscapes(t *testing.T) {
	for _, value := range []string{"https://attacker.example", "//attacker.example", `/\\attacker.example`, "/%5c%5cattacker.example"} {
		if safeLocalReturnURL(value) {
			t.Fatalf("accepted unsafe return URL %q", value)
		}
	}
	for _, value := range []string{"/jobs", "/sites/example/settings?config=queued"} {
		if !safeLocalReturnURL(value) {
			t.Fatalf("rejected local return URL %q", value)
		}
	}
}
