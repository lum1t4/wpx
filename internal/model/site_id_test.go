package model

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewSiteID(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id, err := NewSiteID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id != strings.ToLower(id) {
			t.Fatalf("identifier is not a canonical lowercase UUID: %q", id)
		}
		value, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		if err != nil || len(value) != 16 {
			t.Fatalf("identifier does not contain 16 hexadecimal bytes: %q", id)
		}
		if value[6]>>4 != 4 || value[8]>>6 != 2 {
			t.Fatalf("identifier has incorrect UUID version or variant: %q", id)
		}
		if err := ValidateSiteID(id); err != nil {
			t.Fatalf("generated identifier rejected: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate generated identifier: %q", id)
		}
		seen[id] = true
	}
}

func TestValidateSiteIDUUIDAndLegacyCompatibility(t *testing.T) {
	valid := []string{
		"01234567-89ab-4cde-8f01-23456789abcd",
		"01234567-89ab-4cde-9f01-23456789abcd",
		"01234567-89ab-4cde-af01-23456789abcd",
		"01234567-89ab-4cde-bf01-23456789abcd",
		"abcdef01-2345-4678-9abc-def012345678",
		"abc", "a-b", "site-123", "a" + strings.Repeat("0", 47),
		// This UUID-shaped value was already a valid legacy slug. Do not reject
		// it just because its version would not be emitted by NewSiteID.
		"f47ac10b-58cc-1372-a567-0e02b2c3d479",
	}
	for _, id := range valid {
		if err := ValidateSiteID(id); err != nil {
			t.Errorf("valid site identifier %q rejected: %v", id, err)
		}
	}
	invalid := []string{
		"", "ab", "a" + strings.Repeat("0", 48), "123", "site--id", "site-", "-site", "Site-id", "site_id",
		"01234567-89ab-3cde-8f01-23456789abcd", // Not version 4.
		"01234567-89ab-4cde-cf01-23456789abcd", // Not the RFC 4122 variant.
		"01234567-89AB-4CDE-8F01-23456789ABCD",
		"0123456789ab4cde8f0123456789abcd",
		"{01234567-89ab-4cde-8f01-23456789abcd}",
		"../site", "/site", `site\path`, "site/id", "site\n", "site\x00", " site", "site ",
	}
	for _, id := range invalid {
		if err := ValidateSiteID(id); err == nil {
			t.Errorf("unsafe or malformed site identifier %q accepted", id)
		}
	}
}

func TestValidateSiteWithUUIDs(t *testing.T) {
	site := Site{
		ID:           "01234567-89ab-4cde-8f01-23456789abcd",
		ParentSiteID: "01234567-89ab-4cde-af01-23456789abcd",
		Domain:       "stage.example.com",
		Kind:         WordPress,
		PHPVersion:   "8.4",
		Environment:  "staging",
	}
	if err := ValidateSite(site); err != nil {
		t.Fatalf("digit-leading UUID identifiers must support staging: %v", err)
	}
}
