package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
