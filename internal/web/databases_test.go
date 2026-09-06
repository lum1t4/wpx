//go:build linux

package web

import "testing"

func TestDatabaseAdminCookiesExcludePanelSecrets(t *testing.T) {
	got := databaseAdminCookies("wpx_session=panel-secret; phpMyAdmin=session; wpx_csrf=csrf-secret; pma_lang=en")
	if got != "phpMyAdmin=session; pma_lang=en" {
		t.Fatalf("filtered cookies=%q", got)
	}
}
