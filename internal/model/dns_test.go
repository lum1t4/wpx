package model

import "testing"

func TestDNSRecordValidationRestrictsCloudflareProxying(t *testing.T) {
	record := DNSRecord{Name: "www.example.com", Type: "A", Value: "192.0.2.10", TTL: 300, Proxied: true}
	cloudflare := DNSProvider{Name: "Cloudflare", Kind: DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"}
	if err := ValidateDNSRecord(record, cloudflare); err != nil {
		t.Fatal(err)
	}
	route53 := DNSProvider{Name: "Route 53", Kind: DNSRoute53, ZoneID: "Z123EXAMPLE", AccessKey: "AKIAEXAMPLEACCESS", SecretKey: "a-secret-access-key-that-is-long-enough"}
	if err := ValidateDNSRecord(record, route53); err == nil {
		t.Fatal("Route 53 record unexpectedly accepted Cloudflare proxying")
	}
}

func TestDNSRecordValidationAcceptsWildcardMultisiteAddress(t *testing.T) {
	provider := DNSProvider{Name: "Cloudflare", Kind: DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"}
	record := DNSRecord{Name: "*.network.example.com", Type: "A", Value: "192.0.2.10", TTL: 300, Proxied: true}
	if err := ValidateDNSRecord(record, provider); err != nil {
		t.Fatal(err)
	}
}
