package model

import "testing"

func TestValidateWordPressDebugSite(t *testing.T) {
	site := Site{ID: "example-site", Domain: "example.test", Kind: WordPress, PHPVersion: "8.4", Status: "active"}
	if err := ValidateWordPressDebugSite(site); err != nil {
		t.Fatal(err)
	}
	site.Kind = PHP
	if err := ValidateWordPressDebugSite(site); err == nil {
		t.Fatal("accepted a non-WordPress site")
	}
	site.Kind, site.Status = WordPress, "deleting"
	if err := ValidateWordPressDebugSite(site); err == nil {
		t.Fatal("accepted a site in a destructive lifecycle state")
	}
}
