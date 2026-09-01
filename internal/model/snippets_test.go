package model

import "testing"

func TestValidateSiteSnippetsUsesExplicitDirectiveAllowlists(t *testing.T) {
	site := Site{ID: "example-com", Domain: "example.com", Kind: PHP, PHPVersion: "8.4"}
	valid := SiteSnippets{
		Nginx: "client_max_body_size 128M;\nadd_header X-Frame-Options \"SAMEORIGIN\" always;",
		PHP:   "php_admin_value[memory_limit] = 512M\nphp_admin_flag[display_errors] = off",
	}
	if err := ValidateSiteSnippets(site, valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []SiteSnippets{
		{Nginx: "location /admin { allow all; }"},
		{Nginx: "include /tmp/untrusted.conf;"},
		{PHP: "env[LD_PRELOAD] = /tmp/untrusted.so"},
		{PHP: "php_admin_value[auto_prepend_file] = /tmp/untrusted.php"},
	} {
		if err := ValidateSiteSnippets(site, invalid); err == nil {
			t.Fatalf("accepted unsafe snippets: %#v", invalid)
		}
	}
}
