package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestDNSProviderSecretsAndOwnedRecordLifecycle(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("site job unavailable")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	provider, err := state.CreateDNSProvider(ctx, owner, model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT config_ciphertext FROM dns_providers WHERE id=?`, provider.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("a-cloudflare-token-long-enough")) {
		t.Fatal("Cloudflare token was stored as plaintext")
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.provider_verify" {
		t.Fatalf("provider job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateDNSRecord(ctx, owner, model.DNSRecord{SiteID: "example-com", ProviderID: provider.ID, Name: "example.com", Type: "A", Value: "192.0.2.10", TTL: 300, Proxied: true}); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.record_apply" {
		t.Fatalf("record job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, `{"remote_id":"cloudflare-record"}`, nil); err != nil {
		t.Fatal(err)
	}
	records, err := state.ListSiteDNSRecords(ctx, "example-com")
	if err != nil || len(records) != 1 || records[0].Status != "active" || records[0].RemoteID != "cloudflare-record" {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if _, err := state.UpdateDNSRecord(ctx, owner, "example-com", records[0].ID, model.DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.11", TTL: 600}); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.record_apply" {
		t.Fatalf("update job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", errors.New("provider unavailable")); err != nil {
		t.Fatal(err)
	}
	failed, err := state.DNSRecord(ctx, records[0].ID)
	if err != nil || failed.Status != "failed" || failed.RemoteID != "cloudflare-record" {
		t.Fatalf("failed update lost remote ownership: record=%#v err=%v", failed, err)
	}
	if _, err := state.UpdateDNSRecord(ctx, owner, "example-com", failed.ID, model.DNSRecord{Name: failed.Name, Type: failed.Type, Value: failed.Value, TTL: failed.TTL}); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("retry job unavailable")
	}
	if err := state.FinishJob(ctx, job, `{"remote_id":"cloudflare-record"}`, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.DeleteDNSRecord(ctx, owner, "example-com", failed.ID); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.record_delete" {
		t.Fatalf("delete job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	records, err = state.ListSiteDNSRecords(ctx, "example-com")
	if err != nil || len(records) != 0 {
		t.Fatalf("deleted records=%#v err=%v", records, err)
	}
}

func TestEnqueueDNSCertificateTracksWildcardAndTLSState(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "tls-example", Domain: "tls.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("site job unavailable")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	provider, err := state.CreateDNSProvider(ctx, owner, model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("provider job unavailable")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	jobID, err := state.EnqueueDNSCertificate(ctx, owner, "tls-example", provider.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil || queued.Kind != "site.certificate_dns" || queued.PayloadJSON == "" {
		t.Fatalf("job=%#v err=%v", queued, err)
	}
	if !bytes.Contains([]byte(queued.PayloadJSON), []byte(`"wildcard":true`)) || bytes.Contains([]byte(queued.PayloadJSON), []byte("a-cloudflare-token-long-enough")) {
		t.Fatalf("unsafe or incomplete payload: %s", queued.PayloadJSON)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID {
		t.Fatalf("certificate job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "tls-example")
	if err != nil || site.TLSStatus != "active" {
		t.Fatalf("site=%#v err=%v", site, err)
	}
}
