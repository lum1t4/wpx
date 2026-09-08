package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionedStaticAssetsAreImmutableAndRevalidateByETag(t *testing.T) {
	staticFiles, err := fs.Sub(templateFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.StripPrefix("/assets/", staticAssetHandler(staticFiles))
	url := assetURL("app.css")
	request := httptest.NewRequest(http.MethodGet, url, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", url, recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	etag := recorder.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"sha256-`) {
		t.Fatalf("ETag = %q", etag)
	}

	request = httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("If-None-Match", etag)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want %d", recorder.Code, http.StatusNotModified)
	}
}

func TestUnversionedStaticAssetMustRevalidate(t *testing.T) {
	staticFiles, err := fs.Sub(templateFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
	recorder := httptest.NewRecorder()
	http.StripPrefix("/assets/", staticAssetHandler(staticFiles)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /app.css = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func TestStaleStaticAssetFingerprintMustRevalidate(t *testing.T) {
	staticFiles, err := fs.Sub(templateFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/app.css?v=from-an-older-deployment", nil)
	recorder := httptest.NewRecorder()
	http.StripPrefix("/assets/", staticAssetHandler(staticFiles)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET stale app.css fingerprint = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}
