//go:build linux

package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/store"
)

func testServer(t *testing.T) (*Server, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	state, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ConfigureSecretKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	cfg := config.Default()
	cfg.StatePath = filepath.Join(dir, "state.db")
	cfg.DataRoot = filepath.Join(dir, "data")
	cfg.SiteRoot = filepath.Join(dir, "sites")
	cfg.RunRoot = filepath.Join(dir, "run")
	cfg.BrokerSocket = filepath.Join(dir, "run", "broker.sock")
	digest := sha256.Sum256([]byte("bootstrap-secret"))
	cfg.BootstrapTokenHash = hex.EncodeToString(digest[:])
	ctx, cancel := context.WithCancel(context.Background())
	brokerServer := &broker.Server{SocketPath: cfg.BrokerSocket, SiteRoot: cfg.SiteRoot, AllowedUID: uint32(os.Getuid()), SocketGID: -1}
	go func() { _ = brokerServer.Run(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(cfg.BrokerSocket); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	server, err := New(cfg, state, broker.Client{SocketPath: cfg.BrokerSocket}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return server, cancel
}

func TestSetupCreatesOwnerAndSession(t *testing.T) {
	server, cancel := testServer(t)
	defer cancel()
	ts := httptest.NewTLSServer(server.Handler())
	defer ts.Close()
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Get(ts.URL + "/setup")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var csrf *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "wpx_csrf" {
			csrf = cookie
		}
	}
	if csrf == nil {
		t.Fatal("setup did not issue a CSRF cookie")
	}
	form := url.Values{
		"csrf_token": {csrf.Value}, "bootstrap_token": {"bootstrap-secret"},
		"username": {"operator"}, "password": {"a-secure-test-password"},
		"password_confirm": {"a-secure-test-password"},
	}
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("setup returned %d %q: %s", response.StatusCode, response.Header.Get("Location"), body)
	}
	foundSession := false
	for _, cookie := range response.Cookies() {
		if cookie.Name == "wpx_session" && cookie.HttpOnly && cookie.Secure {
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatal("setup did not issue a protected session")
	}
}

func TestTailwindAssetIsEmbedded(t *testing.T) {
	server, cancel := testServer(t)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "--color-wpx-500") {
		t.Fatalf("asset response is %d with %d bytes", recorder.Code, recorder.Body.Len())
	}
}
