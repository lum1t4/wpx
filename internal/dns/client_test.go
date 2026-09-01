package dns

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestCloudflareUsesScopedBearerTokenAndReturnsRecordID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer a-cloudflare-token-long-enough" || r.URL.Path != "/zones/zone123/dns_records" {
			t.Errorf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"comment":"Managed by WPX"`) {
			t.Errorf("record ownership comment missing: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"result":{"id":"remote123"}}`))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), CloudflareBase: server.URL}
	provider := model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"}
	record := model.DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.10", TTL: 300, Proxied: true}
	remoteID, err := client.Apply(context.Background(), provider, record)
	if err != nil || remoteID != "remote123" {
		t.Fatalf("remote=%q err=%v", remoteID, err)
	}
}

func TestRoute53SignsUpsertAndMarksRecordAsWPXManaged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLEACCESS/") || r.Header.Get("X-Amz-Date") != "20260901T120000Z" {
			t.Errorf("request was not signed: %#v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		for _, expected := range []string{"<Action>UPSERT</Action>", "<Comment>Managed by WPX</Comment>", "<Name>www.example.com</Name>"} {
			if !strings.Contains(string(body), expected) {
				t.Errorf("missing %q in %s", expected, body)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), Route53Base: server.URL, Now: func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }}
	provider := model.DNSProvider{Name: "Route 53", Kind: model.DNSRoute53, ZoneID: "Z123EXAMPLE", AccessKey: "AKIAEXAMPLEACCESS", SecretKey: "a-secret-access-key-that-is-long-enough"}
	record := model.DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.10", TTL: 300}
	remoteID, err := client.Apply(context.Background(), provider, record)
	if err != nil || remoteID != "www.example.com|A" {
		t.Fatalf("remote=%q err=%v", remoteID, err)
	}
}

func TestCloudflareDeleteTargetsOnlyStoredRemoteRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/zones/zone123/dns_records/remote123" {
			t.Errorf("unexpected delete request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"result":{"id":"remote123"}}`))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), CloudflareBase: server.URL}
	provider := model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"}
	record := model.DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.10", TTL: 300, RemoteID: "remote123"}
	if err := client.Delete(context.Background(), provider, record); err != nil {
		t.Fatal(err)
	}
}

func TestRoute53DeleteUsesExactStoredRecordSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, expected := range []string{"<Action>DELETE</Action>", "<Name>www.example.com</Name>", "<TTL>300</TTL>", "<Value>192.0.2.10</Value>"} {
			if !strings.Contains(string(body), expected) {
				t.Errorf("missing %q in %s", expected, body)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), Route53Base: server.URL, Now: func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }}
	provider := model.DNSProvider{Name: "Route 53", Kind: model.DNSRoute53, ZoneID: "Z123EXAMPLE", AccessKey: "AKIAEXAMPLEACCESS", SecretKey: "a-secret-access-key-that-is-long-enough"}
	record := model.DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.10", TTL: 300, RemoteID: "www.example.com|A"}
	if err := client.Delete(context.Background(), provider, record); err != nil {
		t.Fatal(err)
	}
}
