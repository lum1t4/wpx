package store

import (
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSiteSnippetsAreValidatedStoredAndAppliedDurably(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "php-example", Domain: "php.example.com", Kind: model.PHP, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("site job unavailable")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	want := model.SiteSnippets{Nginx: "client_max_body_size 128M;", PHP: "php_admin_value[memory_limit] = 512M"}
	jobID, err := state.SetSiteSnippets(ctx, owner, "php-example", want)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID || job.Kind != "site.config_apply" {
		t.Fatalf("snippet job=%#v found=%v err=%v", job, found, err)
	}
	got, err := state.SiteSnippets(ctx, "php-example")
	if err != nil || got != want {
		t.Fatalf("snippets=%#v err=%v", got, err)
	}
}
