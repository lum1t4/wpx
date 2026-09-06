package model

import "testing"

func TestValidateSite(t *testing.T) {
	valid := []Site{
		{ID: "example-com", Domain: "example.com", Kind: Static},
		{ID: "wp-example", Domain: "wp.example.com", Kind: WordPress, PHPVersion: "8.4"},
		{ID: "wp-network", Domain: "network.example.com", Kind: WordPress, PHPVersion: "8.4", WordPressMultisite: MultisiteSubdomains},
		{ID: "api-example", Domain: "api.example.com", Kind: ReverseProxy, Upstream: "http://127.0.0.1:8080"},
	}
	for _, site := range valid {
		if err := ValidateSite(site); err != nil {
			t.Errorf("%#v: %v", site, err)
		}
	}
	invalid := []Site{
		{ID: "../../etc", Domain: "example.com", Kind: Static},
		{ID: "example-com", Domain: "example.com\ninclude /etc/passwd", Kind: Static},
		{ID: "example-com", Domain: "example.com", Kind: WordPress, PHPVersion: "9.0"},
		{ID: "example-com", Domain: "example.com", Kind: ReverseProxy, Upstream: "http://user:pass@localhost:8080"},
		{ID: "old-php", Domain: "old.example.com", Kind: PHP, PHPVersion: "7.4"},
		{ID: "static-network", Domain: "network.example.com", Kind: Static, WordPressMultisite: MultisiteSubdirectories},
	}
	for _, site := range invalid {
		if err := ValidateSite(site); err == nil {
			t.Errorf("accepted %#v", site)
		}
	}
	if err := ValidateSite(Site{ID: "old-php", Domain: "old.example.com", Kind: PHP, PHPVersion: "7.4", AllowEOL: true}); err != nil {
		t.Fatalf("advanced EOL confirmation was rejected: %v", err)
	}
}

func TestPHPVersionRangeAndEOLConfirmation(t *testing.T) {
	for _, version := range []string{"7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"} {
		t.Run(version, func(t *testing.T) {
			if !ValidPHPVersion(version) {
				t.Fatalf("supported PHP version %s was rejected", version)
			}
			site := Site{ID: "legacy-php", Domain: "legacy.example.com", Kind: PHP, PHPVersion: version}
			if err := ValidateSite(site); (err != nil) != IsEOLPHP(version) {
				t.Fatalf("PHP %s without EOL confirmation: %v", version, err)
			}
			site.AllowEOL = true
			if err := ValidateSite(site); err != nil {
				t.Fatalf("supported PHP %s with EOL confirmation: %v", version, err)
			}
		})
	}
	for _, version := range []string{"", "7.0", "7.5", "8.6", "9.0", "8.0.1", "08.0"} {
		if ValidPHPVersion(version) {
			t.Errorf("unsupported PHP version %q was accepted", version)
		}
	}
}
