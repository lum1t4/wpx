//go:build linux

package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

func TestWordPressDebugPageReadsBoundedBrokerResults(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	privileged.run = func(operation broker.Operation, _, output any) error {
		switch operation {
		case broker.OpWordPressDebugStatus:
			*output.(*model.WordPressDebugStatus) = model.WordPressDebugStatus{Enabled: true, Known: true, Managed: true, LogExists: true, LogSize: 12}
		case broker.OpWordPressDebugRead:
			*output.(*model.WordPressDebugLog) = model.WordPressDebugLog{Content: "safe debug entry", Size: 12}
		default:
			return errors.New("unexpected operation")
		}
		return nil
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/debug-site/wordpress/debug", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "safe debug entry") || !strings.Contains(response.Body.String(), "Errors stay hidden from visitors") {
		t.Fatalf("debug page = %d: %s", response.Code, response.Body.String())
	}
	if len(privileged.calls) != 2 || privileged.calls[0] != broker.OpWordPressDebugStatus || privileged.calls[1] != broker.OpWordPressDebugRead {
		t.Fatalf("broker calls = %v", privileged.calls)
	}
}

func TestWordPressDebugMutationsRequireCSRFAndValidateBoolean(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	privileged.run = func(operation broker.Operation, _, _ any) error {
		if operation != broker.OpWordPressDebugSet {
			return errors.New("unexpected operation")
		}
		return nil
	}
	form := url.Values{"enabled": {"true"}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/debug-site/wordpress/debug", form)
	if response.Code != http.StatusSeeOther || len(privileged.calls) != 1 {
		t.Fatalf("valid mutation = %d calls=%v", response.Code, privileged.calls)
	}
	privileged.calls = nil
	form.Set("enabled", "yes")
	response = navigationRequest(t, server, owner, http.MethodPost, "/sites/debug-site/wordpress/debug", form)
	if response.Code != http.StatusBadRequest || len(privileged.calls) != 0 {
		t.Fatalf("invalid boolean = %d calls=%v", response.Code, privileged.calls)
	}
	request := httptest.NewRequest(http.MethodPost, "/sites/debug-site/wordpress/debug", strings.NewReader(url.Values{"enabled": {"false"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	session, _ := server.store.CreateSession(request.Context(), owner.ID, time.Hour)
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: session})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(privileged.calls) != 0 {
		t.Fatalf("missing CSRF = %d calls=%v", response.Code, privileged.calls)
	}
}

func TestWordPressDebugBrokerErrorsDoNotLeakPrivateDetails(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	privileged.run = func(broker.Operation, any, any) error { return errors.New("/srv/private/password=secret") }
	response := navigationRequest(t, server, owner, http.MethodGet, "/sites/debug-site/wordpress/debug", nil)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "/srv/private") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("private broker error leaked: %d %s", response.Code, response.Body.String())
	}
}

func TestWordPressDebugRejectsOversizedMutationBeforeBroker(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	form := url.Values{"enabled": {"true"}, "padding": {strings.Repeat("x", 9<<10)}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/debug-site/wordpress/debug", form)
	if response.Code != http.StatusRequestEntityTooLarge || len(privileged.calls) != 0 {
		t.Fatalf("oversized mutation = %d calls=%v", response.Code, privileged.calls)
	}
}
