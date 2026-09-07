//go:build linux

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func fileManagerRequest(t *testing.T, server *Server, user store.User, method, target, contentType, csrf string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token, err := server.store.CreateSession(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: "wpx_csrf", Value: csrf})
	}
	request.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestFileManagerSearchAuthorizesAndBoundsQuery(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "search-files", Domain: "search.example.com", Kind: model.Static})
	privileged.run = func(op broker.Operation, input, output any) error {
		if op != broker.OpFileSearch {
			t.Fatalf("operation = %q", op)
		}
		request := input.(broker.FileSearchRequest)
		if request.Site.ID != "search-files" || request.Path != "assets" || request.Query != "logo" || request.Limit != fileSearchLimit {
			t.Fatalf("search request = %+v", request)
		}
		*output.(*broker.FileSearchResult) = broker.FileSearchResult{Entries: []broker.FileEntry{{Name: "logo.svg", Path: "assets/logo.svg", Size: 42}}}
		return nil
	}
	response := fileManagerRequest(t, server, owner, http.MethodGet, "/sites/search-files/files/api/search?path=assets&q=logo", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"path":"assets/logo.svg"`) {
		t.Fatalf("search response = %d %s", response.Code, response.Body.String())
	}

	privileged.calls = nil
	response = fileManagerRequest(t, server, owner, http.MethodGet, "/sites/search-files/files/api/search?q=", "", "", nil)
	if response.Code != http.StatusBadRequest || len(privileged.calls) != 0 {
		t.Fatalf("invalid search reached broker: status=%d calls=%v", response.Code, privileged.calls)
	}
}

func TestFileManagerMutationsRequireCSRF(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "mutate-files", Domain: "mutate.example.com", Kind: model.Static})
	payload, _ := json.Marshal(filePathsInput{Paths: []string{"index.html"}})
	for _, csrf := range []string{"", strings.Repeat("b", 63)} {
		response := fileManagerRequest(t, server, owner, http.MethodPost, "/sites/mutate-files/files/api/delete", "application/json", csrf, payload)
		if response.Code != http.StatusForbidden {
			t.Fatalf("csrf %q status = %d", csrf, response.Code)
		}
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("rejected mutations reached broker: %v", privileged.calls)
	}
}

func TestFileManagerEveryEndpointRequiresSiteFileCapability(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "private-files", Domain: "private-files.example.com", Kind: model.Static})
	customer, err := server.store.CreateUser(context.Background(), owner, "files-customer", "navigation-test-password", rbac.Customer, []string{"private-files"})
	if err != nil {
		t.Fatal(err)
	}
	csrf := strings.Repeat("a", 64)
	requests := []struct {
		method, target, contentType string
		body                        []byte
	}{
		{http.MethodGet, "/sites/private-files/files/api/list", "", nil},
		{http.MethodGet, "/sites/private-files/files/api/search?q=x", "", nil},
		{http.MethodGet, "/sites/private-files/files/api/download?path=x", "", nil},
		{http.MethodPost, "/sites/private-files/files/api/upload?path=x&upload_id=abcdefghijklmnop&offset=0&final=1", "application/octet-stream", nil},
		{http.MethodPost, "/sites/private-files/files/api/mkdir", "application/json", []byte(`{"path":"new"}`)},
		{http.MethodPost, "/sites/private-files/files/api/delete", "application/json", []byte(`{"paths":["x"]}`)},
		{http.MethodPost, "/sites/private-files/files/api/rename", "application/json", []byte(`{"path":"x","new_name":"y"}`)},
		{http.MethodPost, "/sites/private-files/files/api/archive", "application/json", []byte(`{"paths":["x"],"destination":"x.zip"}`)},
		{http.MethodPost, "/sites/private-files/files/api/extract", "application/json", []byte(`{"path":"x.zip","destination":""}`)},
		{http.MethodPost, "/sites/private-files/files/api/copy", "application/json", []byte(`{"paths":["x"],"destination":"","overwrite":false}`)},
		{http.MethodPost, "/sites/private-files/files/api/move", "application/json", []byte(`{"paths":["x"],"destination":"","overwrite":false}`)},
	}
	for _, test := range requests {
		response := fileManagerRequest(t, server, customer, test.method, test.target, test.contentType, csrf, test.body)
		if response.Code != http.StatusForbidden {
			t.Errorf("%s %s status = %d", test.method, test.target, response.Code)
		}
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("unauthorized endpoints reached broker: %v", privileged.calls)
	}
}

func TestFileManagerUploadIsChunkBounded(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "upload-files", Domain: "upload.example.com", Kind: model.Static})
	csrf := strings.Repeat("a", 64)
	target := "/sites/upload-files/files/api/upload?path=large.bin&upload_id=abcdefghijklmnop&offset=0&final=0"
	response := fileManagerRequest(t, server, owner, http.MethodPost, target, "application/octet-stream", csrf, make([]byte, broker.FileChunkBytes+1))
	if response.Code != http.StatusRequestEntityTooLarge || len(privileged.calls) != 0 {
		t.Fatalf("oversized upload status=%d calls=%v", response.Code, privileged.calls)
	}
}

func TestFileManagerStreamsValidatedDownloadChunks(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "download-files", Domain: "download.example.com", Kind: model.Static})
	privileged.run = func(op broker.Operation, input, output any) error {
		if op != broker.OpFileDownload {
			t.Fatalf("operation = %q", op)
		}
		request := input.(broker.FileDownloadRequest)
		part := output.(*broker.FileDownloadResult)
		if request.Offset == 0 {
			*part = broker.FileDownloadResult{Data: []byte("hello "), Offset: 0, Next: 6, Size: 11}
		} else {
			*part = broker.FileDownloadResult{Data: []byte("world"), Offset: 6, Next: 11, Size: 11, EOF: true}
		}
		return nil
	}
	response := fileManagerRequest(t, server, owner, http.MethodGet, "/sites/download-files/files/api/download?path=hello.txt", "", "", nil)
	if response.Code != http.StatusOK || response.Body.String() != "hello world" || response.Header().Get("Content-Length") != "11" {
		t.Fatalf("download response = %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}
