//go:build linux

package web

import (
	"net/http"
	"testing"
)

func TestDatabaseAdminCookiesExcludePanelSecrets(t *testing.T) {
	got := databaseAdminCookies("wpx_session=panel-secret; __Secure-phpMyAdmin_https=session; wpx_csrf=csrf-secret; __Secure-pma_lang_https=en")
	if got != "__Secure-phpMyAdmin_https=session; __Secure-pma_lang_https=en" {
		t.Fatalf("filtered cookies=%q", got)
	}
}

func TestDatabaseAdminResponseCookiesAreSecureAndCannotReplacePanelSession(t *testing.T) {
	response := &http.Response{Header: http.Header{"Set-Cookie": {
		"__Secure-phpMyAdmin_https=session-id; Path=/; HttpOnly; SameSite=Lax",
		"WPXSignon=signon-id; Path=/phpmyadmin/; HttpOnly",
		"wpx_session=attacker-value; Path=/; HttpOnly",
	}}}
	if err := scopeDatabaseAdminCookies(response); err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	if len(cookies) != 2 {
		t.Fatalf("scoped cookies=%v", response.Header.Values("Set-Cookie"))
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.Path != "/phpmyadmin/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie was not securely scoped: %#v", cookie)
		}
		if cookie.Name == "wpx_session" || cookie.Name == "wpx_csrf" {
			t.Fatalf("panel cookie escaped response filter: %#v", cookie)
		}
	}
}
