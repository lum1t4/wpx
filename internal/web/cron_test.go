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

func TestCronRoutesRequireAssignmentAndOwnerCapability(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static})
	navigationSite(t, server, owner, model.Site{ID: "private-cron", Domain: "private.example.com", Kind: model.Static})
	customer, err := server.store.CreateUser(context.Background(), owner, "cron-customer", "strong-test-password", rbac.Customer, []string{"cron-site"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/sites/cron-site/cron", "/sites/private-cron/cron"} {
		response := navigationRequest(t, server, customer, http.MethodGet, target, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("customer GET %s = %d", target, response.Code)
		}
	}
	form := url.Values{"name": {"task"}, "preset": {"hourly"}, "executable": {"php"}, "arguments": {"task"}, "enabled": {"yes"}}
	response := navigationRequest(t, server, customer, http.MethodPost, "/sites/cron-site/cron", form)
	if response.Code != http.StatusForbidden || len(privileged.calls) != 0 {
		t.Fatalf("unauthorized mutation = %d, calls=%v", response.Code, privileged.calls)
	}
}

func TestCronCreatePersistsVectorAndRejectsShell(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static})
	form := url.Values{"name": {"Laravel scheduler"}, "preset": {"custom"}, "minute": {"*/5"}, "hour": {"*"}, "day_of_month": {"*"}, "month": {"*"}, "day_of_week": {"*"}, "executable": {"php"}, "arguments": {"artisan\nschedule:run --verbose"}, "enabled": {"yes"}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/cron-site/cron", form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", response.Code, response.Body.String())
	}
	schedules, err := server.store.ListCronSchedules(context.Background(), "cron-site")
	if err != nil || len(schedules) != 1 || schedules[0].ApplyStatus != "pending" || strings.Join(schedules[0].Command, "|") != "php|artisan|schedule:run --verbose" {
		t.Fatalf("stored = %+v, %v", schedules, err)
	}
	privileged.calls = nil
	form.Set("executable", "/bin/sh")
	form.Set("arguments", "-c\nid")
	response = navigationRequest(t, server, owner, http.MethodPost, "/sites/cron-site/cron", form)
	if response.Code != http.StatusBadRequest || len(privileged.calls) != 0 {
		t.Fatalf("shell create = %d calls=%v", response.Code, privileged.calls)
	}
}

func TestCronCustomScheduleRequiresFiveStrictFields(t *testing.T) {
	server, owner, _ := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static})
	form := url.Values{"name": {"bad"}, "preset": {"custom"}, "minute": {"60"}, "hour": {"*"}, "day_of_month": {"*"}, "month": {"*"}, "day_of_week": {"*"}, "executable": {"php"}, "arguments": {"task"}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/cron-site/cron", form)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid cron field 1") {
		t.Fatalf("invalid custom response = %d: %s", response.Code, response.Body.String())
	}
}

func TestCronMutationRejectsMissingCSRFBeforeStateOrBroker(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	token, err := server.store.CreateSession(context.Background(), owner.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"name": {"task"}, "preset": {"hourly"}, "executable": {"php"}, "arguments": {"task"}, "enabled": {"yes"}}
	request := httptest.NewRequest(http.MethodPost, "/sites/cron-site/cron", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(privileged.calls) != 0 {
		t.Fatalf("missing CSRF = %d, calls=%v", response.Code, privileged.calls)
	}
	schedules, err := server.store.ListCronSchedules(context.Background(), "cron-site")
	if err != nil || len(schedules) != 0 {
		t.Fatalf("missing CSRF changed schedules: %+v, %v", schedules, err)
	}
	setting, err := server.store.WordPressCronSetting(context.Background(), "cron-site")
	if err != nil || setting.Replaced {
		t.Fatalf("missing CSRF changed WordPress cron: %+v, %v", setting, err)
	}
}
