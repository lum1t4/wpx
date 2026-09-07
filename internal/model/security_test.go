package model

import "testing"

func TestValidateSecuritySettingsRequiresWordPressAndSelectedProtection(t *testing.T) {
	wordpress := Site{ID: "wp-site", Domain: "wp.example.com", Kind: WordPress, PHPVersion: "8.4", Status: "active"}
	if err := ValidateSecuritySettings(wordpress, SecuritySettings{Enabled: true}); err == nil {
		t.Fatal("enabled policy without a protection was accepted")
	}
	settings := DefaultSecuritySettings()
	settings.Enabled = true
	if err := ValidateSecuritySettings(wordpress, settings); err != nil {
		t.Fatal(err)
	}
	php := wordpress
	php.Kind = PHP
	if err := ValidateSecuritySettings(php, settings); err == nil {
		t.Fatal("non-WordPress site was accepted")
	}
}
