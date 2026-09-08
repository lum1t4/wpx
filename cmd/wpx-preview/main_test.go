package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/store"
	"github.com/lum1t4/wpx/internal/web"
)

func TestPreviewRejectsWritesBeforeCallingPanel(t *testing.T) {
	called := false
	handler := readOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "sample-session")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/sites", nil))
		if called || response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s reached the panel or returned %d", method, response.Code)
		}
	}
}

func TestPreviewUsesItsOwnSessionAndMarksSampleHTML(t *testing.T) {
	handler := readOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("wpx_session")
		if err != nil || cookie.Value != "sample-session" || len(r.Cookies()) != 1 {
			t.Fatal("preview did not replace incoming credentials")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><main>Sample</main>"))
	}), "sample-session")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Cookie", "wpx_session=untrusted; other=value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Preview · sample data · read-only") || response.Header().Get("X-WPX-Preview") != "sample-data" {
		t.Fatal("preview response is not visibly marked as sample data")
	}
}

func TestFeaturePreviewPagesExposeRepresentativeReadOnlyStates(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.db")
	state, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	owner, err := seed(context.Background(), state, statePath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := state.CreateSession(context.Background(), owner.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	panel, err := web.New(config.Config{StatePath: statePath, DataRoot: directory, SiteRoot: filepath.Join(directory, "sites")}, state, sampleBroker{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler := readOnly(panel.Handler(), token)

	tests := []struct {
		path string
		want []string
	}{
		{path: "/sites/northstar/wordpress/debug", want: []string{"Debug mode", "Enabled", "sample log entry for visual review"}},
		{path: "/sites/northstar/wordpress/search-replace", want: []string{"Search and replace", "Offsite backups", "Preview changes"}},
		{path: "/sites/northstar-staging/backups", want: []string{"Backup history", "Running", "Queued", "Success", "Failed", "No longer retained", "Restore point ready"}},
		{path: "/account/security?username=saved", want: []string{"owner@example.com", "Username updated.", `action="/account/username"`}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", test.path, response.Code, response.Body.String())
			}
			for _, want := range test.want {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("GET %s did not include %q", test.path, want)
				}
			}
		})
	}
}
