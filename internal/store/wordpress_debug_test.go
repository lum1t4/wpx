package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestWordPressDebugAuditRequiresSiteManagementAndStoresNoLogData(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "debug-owner", "long-enough-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if _, err := state.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	customer, err := state.CreateUser(ctx, owner, "debug-customer", "long-enough-test-password", rbac.Customer, []string{site.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RecordWordPressDebugEvent(ctx, customer, site.ID, "wordpress.debug_enabled", true); err == nil {
		t.Fatal("customer recorded a WordPress debug change")
	}
	if err := state.RecordWordPressDebugEvent(ctx, owner, site.ID, "wordpress.debug_enabled", true); err != nil {
		t.Fatal(err)
	}
	var action, result, detail string
	if err := state.db.QueryRowContext(ctx, `SELECT action,result,COALESCE(detail_json,'') FROM audit_events WHERE target_id=? AND action='wordpress.debug_enabled'`, site.ID).Scan(&action, &result, &detail); err != nil {
		t.Fatal(err)
	}
	if action != "wordpress.debug_enabled" || result != "success" || (detail != "" && detail != "{}") {
		t.Fatalf("audit event contains unexpected data: %q %q %q", action, result, detail)
	}
}
