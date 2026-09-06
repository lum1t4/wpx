//go:build linux

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGoogleDriveSetupUsesBrowserOAuthAndCreatesTarget(t *testing.T) {
	server, cancel := testServer(t)
	defer cancel()
	owner, err := server.store.CreateOwner(t.Context(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	csrf := strings.Repeat("a", 64)
	form := url.Values{
		"csrf_token": {csrf}, "name": {"Drive"}, "drive_folder": {"WPX Backups"},
		"google_client_id": {"123.apps.googleusercontent.com"}, "google_client_secret": {"very-secret-client-value"},
	}
	request := httptest.NewRequest(http.MethodPost, "https://panel.example.com/backups/google-drive/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: csrf})
	recorder := httptest.NewRecorder()
	server.googleAuthURL = "https://accounts.example.test/auth"
	server.startGoogleDriveOAuth(recorder, request, owner)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("start returned %d: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if query.Get("state") == "" || query.Get("code_challenge") == "" || query.Get("redirect_uri") != "https://panel.example.com/backups/google-drive/callback" || strings.Contains(location.String(), "very-secret") {
		t.Fatalf("unsafe or incomplete authorization URL: %s", location)
	}

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token method=%s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		if values.Get("client_secret") != "very-secret-client-value" || values.Get("code_verifier") == "" || values.Get("code") != "google-code" {
			t.Errorf("token form=%v", values)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer",
			"scope": "https://www.googleapis.com/auth/drive.file", "expires_in": 3600,
		})
	}))
	defer tokenServer.Close()
	server.googleTokenURL = tokenServer.URL
	server.oauthHTTP = tokenServer.Client()
	callback := httptest.NewRequest(http.MethodGet, "https://panel.example.com/backups/google-drive/callback?state="+url.QueryEscape(query.Get("state"))+"&code=google-code", nil)
	callbackRecorder := httptest.NewRecorder()
	server.googleDriveOAuthCallback(callbackRecorder, callback)
	if callbackRecorder.Code != http.StatusOK || !strings.Contains(callbackRecorder.Body.String(), "Google Drive is connected") {
		t.Fatalf("callback returned %d: %s", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	targets, err := server.store.ListBackupTargets(t.Context())
	if err != nil || len(targets) != 1 || targets[0].Kind != "google_drive" {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	if _, err := server.store.ConsumeGoogleDriveOAuthFlow(t.Context(), query.Get("state")); err == nil {
		t.Fatal("callback state remained reusable")
	}
}
