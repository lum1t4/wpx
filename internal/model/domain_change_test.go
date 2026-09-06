package model

import "testing"

func TestDomainChangeKeepsPersistedPreviousState(t *testing.T) {
	site := Site{ID: "example-site", Domain: "example.com", Kind: Static, TLSStatus: "active"}
	change := DomainChange{PreviousDomain: site.Domain, Domain: "new.example.com", PreviousTLSStatus: "active"}
	if err := ValidateDomainChange(site, change); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*DomainChange){
		func(c *DomainChange) { c.PreviousDomain = "other.example.com" },
		func(c *DomainChange) { c.PreviousTLSStatus = "none" },
		func(c *DomainChange) { c.Domain = site.Domain },
		func(c *DomainChange) { c.Domain = "New.example.com" },
		func(c *DomainChange) { c.Domain = "new.example.com; return 200;" },
		func(c *DomainChange) { c.Domain = "new.example.com\n" },
		func(c *DomainChange) { c.Domain = "https://new.example.com" },
	} {
		invalid := change
		mutate(&invalid)
		if err := ValidateDomainChange(site, invalid); err == nil {
			t.Fatalf("unsafe change accepted: %#v", invalid)
		}
	}
	site.Kind, site.PHPVersion, site.WordPressMultisite = WordPress, "8.4", MultisiteSubdirectories
	if err := ValidateDomainChange(site, change); err == nil {
		t.Fatal("multisite rename accepted")
	}
}
