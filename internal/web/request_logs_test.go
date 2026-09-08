//go:build linux

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

const (
	request200 = `192.0.2.10 - - [08/Sep/2026:14:05:06 +0200] "GET /products?q=lamp HTTP/1.1" 200 1234 "-" "browser"`
	request404 = `2001:db8::7 - - [08/Sep/2026:14:06:07 +0200] "POST /checkout\x20now?next=\x3Cscript\x3E HTTP/2.0" 404 0 "https://example.com" "browser"`
	request503 = `192.0.2.10 - - [08/Sep/2026:14:07:08 +0200] "GET /health HTTP/1.1" 503 - "-" "monitor"`
)

func TestParseNginxAccessLogPreservesEscapedRequestTarget(t *testing.T) {
	entry, ok := parseNginxAccessLog(request404)
	if !ok {
		t.Fatal("valid combined log line was rejected")
	}
	if entry.IP != "2001:db8::7" || entry.Method != "POST" || entry.Path != `/checkout\x20now?next=\x3Cscript\x3E` || entry.Status != 404 || !entry.HasBytes || entry.Bytes != 0 || entry.Timestamp != "2026-09-08 14:06:07 +02:00" {
		t.Fatalf("unexpected parsed request: %#v", entry)
	}
	for _, malformed := range []string{"", "not a log", `bad-ip - - [08/Sep/2026:14:05:06 +0200] "GET / HTTP/1.1" 200 1`, `192.0.2.1 - - [bad] "GET / HTTP/1.1" 200 1`} {
		if _, ok := parseNginxAccessLog(malformed); ok {
			t.Fatalf("malformed log accepted: %q", malformed)
		}
	}
}

func TestRequestLogFiltersComposeAndKeepShareableValues(t *testing.T) {
	lines := []string{request200, "malformed", request404, request503}
	tests := []struct {
		query string
		want  []int
	}{
		{"", []int{503, 404, 200}},
		{"?ip=192.0.2.10", []int{503, 200}},
		{"?ip=2001:db8::7", []int{404}},
		{"?status=errors", []int{503, 404}},
		{"?status=4xx", []int{404}},
		{"?status=503", []int{503}},
		{"?method=post&path=checkout", []int{404}},
		{"?ip=192.0.2.10&status=5xx&method=GET&path=health", []int{503}},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/logs"+test.query, nil)
			view, err := requestLogView(request, lines)
			if err != nil {
				t.Fatal(err)
			}
			if view.ParsedCount != 3 || view.Skipped != 1 || len(view.Rows) != len(test.want) {
				t.Fatalf("view=%#v want statuses=%v", view, test.want)
			}
			for i, status := range test.want {
				if view.Rows[i].Status != status {
					t.Fatalf("row %d status=%d want=%d", i, view.Rows[i].Status, status)
				}
			}
		})
	}
}

func TestRequestLogFiltersRejectInvalidAndBoundInput(t *testing.T) {
	for _, query := range []string{"?ip=all", "?status=999", "?status=4x", "?method=GET%20POST", "?path=" + strings.Repeat("a", 257), "?path=one%0Atwo"} {
		request := httptest.NewRequest(http.MethodGet, "/logs"+query, nil)
		if _, err := requestLogView(request, []string{request200}); err == nil {
			t.Fatalf("invalid filter accepted: %s", query)
		}
	}
	invalid, err := requestLogView(httptest.NewRequest(http.MethodGet, "/logs?ip=not-an-ip&status=errors&method=get&path=checkout", nil), nil)
	if err == nil || invalid.Filters.IP != "not-an-ip" || invalid.Filters.Status != "errors" || invalid.Filters.Method != "GET" || invalid.Filters.Path != "checkout" || invalid.Error == "" {
		t.Fatalf("invalid filter state was not retained: view=%#v err=%v", invalid, err)
	}
	if invalid.AllQuery != "?ip=not-an-ip&method=GET&path=checkout" || invalid.ErrorsQuery != "?ip=not-an-ip&method=GET&path=checkout&status=errors" {
		t.Fatalf("preset links did not preserve the other filters: %#v", invalid)
	}
	lines := make([]string, 205)
	for i := range lines {
		lines[i] = request200
	}
	view, err := requestLogView(httptest.NewRequest(http.MethodGet, "/logs", nil), lines)
	if err != nil || view.ParsedCount != maxRequestLogLines || len(view.Rows) != maxRequestLogLines {
		t.Fatalf("bounded view=%#v err=%v", view, err)
	}
}

func TestObservabilityFiltersAuthorizeBeforeParsingAndRenderEscapedPaths(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	navigationSite(t, server, owner, model.Site{ID: "logs-site", Domain: "logs.example.com", Kind: model.Static})
	navigationSite(t, server, owner, model.Site{ID: "private-logs", Domain: "private.example.com", Kind: model.Static})
	customer, err := server.store.CreateUser(context.Background(), owner, "logs-customer", "navigation-test-password", rbac.Customer, []string{"logs-site"})
	if err != nil {
		t.Fatal(err)
	}
	privileged.run = func(operation broker.Operation, _, output any) error {
		if operation != broker.OpSiteObservability {
			return fmt.Errorf("unexpected operation %s", operation)
		}
		*output.(*broker.SiteObservabilityResult) = broker.SiteObservabilityResult{AccessLog: []string{request404}}
		return nil
	}
	response := navigationRequest(t, server, customer, http.MethodGet, "/sites/logs-site/observability?status=errors&path=checkout", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if !strings.Contains(body, `/checkout\x20now?next=\x3Cscript\x3E`) || strings.Contains(body, "<script>") || !strings.Contains(body, "404") {
		t.Fatalf("request table lost or unsafely rendered the path: %s", body)
	}

	before := len(privileged.calls)
	requireNavigationStatus(t, navigationRequest(t, server, customer, http.MethodGet, "/sites/private-logs/observability?ip=invalid", nil), http.StatusForbidden)
	if len(privileged.calls) != before {
		t.Fatal("unauthorized filter request crossed the broker boundary")
	}
	invalid := navigationRequest(t, server, owner, http.MethodGet, "/sites/logs-site/observability?ip=invalid", nil)
	requireNavigationStatus(t, invalid, http.StatusBadRequest)
	if body := invalid.Body.String(); !strings.Contains(body, `value="invalid"`) || !strings.Contains(body, "IP filter must be one exact IPv4 or IPv6 address") || !strings.Contains(body, "Request logs") {
		t.Fatalf("invalid filter did not render in context: %s", body)
	}
	if len(privileged.calls) != before {
		t.Fatal("invalid filter request crossed the broker boundary")
	}
}
