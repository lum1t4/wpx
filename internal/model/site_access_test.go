package model

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestSiteAccessValidation(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("a-long-test-password"), 12)
	if err != nil {
		t.Fatal(err)
	}
	valid := SiteAccessSettings{SiteID: "example-site", BasicAuthEnabled: true, Username: "deploy.user", PasswordHash: string(hash), CloudflareOnly: true, Status: "pending"}
	if err := ValidateSiteAccessSettings(valid); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	for _, username := range []string{"", "with:colon", "space name", "../../root"} {
		candidate := valid
		candidate.Username = username
		if ValidateSiteAccessSettings(candidate) == nil {
			t.Fatalf("unsafe username accepted: %q", username)
		}
	}
	weak, err := bcrypt.GenerateFromPassword([]byte("a-long-test-password"), 10)
	if err != nil {
		t.Fatal(err)
	}
	valid.PasswordHash = string(weak)
	if ValidateSiteAccessSettings(valid) == nil {
		t.Fatal("weak password hash accepted")
	}
}

func TestSiteAccessPasswordBoundary(t *testing.T) {
	if ValidateSiteAccessPassword("short") == nil {
		t.Fatal("short password accepted")
	}
	if ValidateSiteAccessPassword(" leading-password") == nil {
		t.Fatal("leading whitespace accepted")
	}
	if err := ValidateSiteAccessPassword("a-long-test-password"); err != nil {
		t.Fatal(err)
	}
}
