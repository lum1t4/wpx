package model

import (
	"errors"
	"net"
	"regexp"
	"strings"
)

type DNSProviderKind string

const (
	DNSCloudflare DNSProviderKind = "cloudflare"
	DNSRoute53    DNSProviderKind = "route53"
)

type DNSProvider struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Kind         DNSProviderKind `json:"kind"`
	Status       string          `json:"status"`
	ZoneID       string          `json:"zone_id"`
	APIToken     string          `json:"api_token,omitempty"`
	AccessKey    string          `json:"access_key,omitempty"`
	SecretKey    string          `json:"secret_key,omitempty"`
	SessionToken string          `json:"session_token,omitempty"`
}

type DNSRecord struct {
	ID         string `json:"id"`
	SiteID     string `json:"site_id"`
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
	Proxied    bool   `json:"proxied"`
	RemoteID   string `json:"remote_id,omitempty"`
	Status     string `json:"status"`
}

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9/_-]{3,128}$`)

func ValidateDNSProvider(provider DNSProvider) error {
	if len(strings.TrimSpace(provider.Name)) < 2 || len(provider.Name) > 64 || !providerIDPattern.MatchString(provider.ZoneID) {
		return errors.New("invalid DNS provider name or zone identifier")
	}
	switch provider.Kind {
	case DNSCloudflare:
		if len(provider.APIToken) < 20 || provider.AccessKey != "" || provider.SecretKey != "" || provider.SessionToken != "" {
			return errors.New("Cloudflare requires a scoped API token")
		}
	case DNSRoute53:
		if len(provider.AccessKey) < 16 || len(provider.SecretKey) < 32 || provider.APIToken != "" {
			return errors.New("Route 53 requires AWS access credentials")
		}
	default:
		return errors.New("invalid DNS provider kind")
	}
	return nil
}

func ValidateDNSRecord(record DNSRecord, provider DNSProvider) error {
	name := record.Name
	if strings.HasPrefix(name, "*.") {
		name = strings.TrimPrefix(name, "*.")
	}
	if !domainPattern.MatchString(name) || record.TTL < 60 || record.TTL > 86400 {
		return errors.New("invalid DNS record name or TTL")
	}
	switch record.Type {
	case "A":
		if ip := net.ParseIP(record.Value); ip == nil || ip.To4() == nil {
			return errors.New("A record requires an IPv4 address")
		}
	case "AAAA":
		if ip := net.ParseIP(record.Value); ip == nil || ip.To4() != nil {
			return errors.New("AAAA record requires an IPv6 address")
		}
	case "CNAME":
		if !domainPattern.MatchString(strings.TrimSuffix(record.Value, ".")) {
			return errors.New("CNAME record requires a hostname")
		}
	case "TXT":
		if record.Value == "" || len(record.Value) > 1024 || strings.ContainsAny(record.Value, "\r\n") {
			return errors.New("invalid TXT record value")
		}
	default:
		return errors.New("unsupported DNS record type")
	}
	if record.Proxied && (provider.Kind != DNSCloudflare || (record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME")) {
		return errors.New("proxying is available only for Cloudflare address and CNAME records")
	}
	return nil
}
