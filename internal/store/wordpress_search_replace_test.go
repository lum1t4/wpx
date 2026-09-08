package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func searchReplacePreview(t *testing.T, state *Store, ctx context.Context, actor User, siteID, targetID string, change model.WordPressSearchReplace) string {
	t.Helper()
	token, err := state.CreateWordPressSearchReplacePreview(ctx, actor, siteID, targetID, change, broker.WordPressSearchReplaceResult{Tables: 2, Replacements: 7, TableResults: []broker.WordPressSearchReplaceTableResult{{Name: "wp_options", Replacements: 2}, {Name: "wp_posts", Replacements: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestWordPressSearchReplaceConsumesBoundPreviewAndQueuesRecoveryJob(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	change := model.WordPressSearchReplace{Search: "private-old-value.example", Replace: "private-new-value.example"}
	token := searchReplacePreview(t, state, ctx, owner, "fleet-one", target.ID, change)
	jobID, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, change, token)
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "wordpress.search_replace" || job.TargetID != "fleet-one" || job.Status != "queued" || !strings.Contains(job.PayloadJSON, `"target_id":"`+target.ID+`"`) {
		t.Fatalf("unexpected search and replace job: %#v", job)
	}
	if strings.Contains(job.PayloadJSON, change.Search) || strings.Contains(job.PayloadJSON, change.Replace) || !strings.Contains(job.PayloadJSON, `"change_ciphertext"`) {
		t.Fatalf("search and replacement values were not encrypted at rest: %s", job.PayloadJSON)
	}
	decodedTarget, decodedChange, err := state.WordPressSearchReplaceJob(job)
	if err != nil || decodedTarget != target.ID || decodedChange != change {
		t.Fatalf("decrypt queued change: target=%q change=%#v error=%v", decodedTarget, decodedChange, err)
	}
	if _, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, change, token); err == nil || (!strings.Contains(err.Error(), "operations") && !strings.Contains(err.Error(), "new search and replace preview")) {
		// The pending durable job and one-use token both reject a replay; query
		// order may surface either invariant without inserting another job.
		t.Fatalf("preview replay was accepted: %v", err)
	}
	var audits string
	if err := state.db.QueryRowContext(ctx, `SELECT detail_json FROM audit_events WHERE action='wordpress.search_replace_requested'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audits, change.Search) || strings.Contains(audits, change.Replace) {
		t.Fatalf("literal values leaked into audit detail: %s", audits)
	}
}

func TestWordPressSearchReplaceRejectsAlteredExpiredAndCrossSitePreviews(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	change := model.WordPressSearchReplace{Search: "old.example", Replace: "new.example"}
	for name, apply := range map[string]func(string) error{
		"altered value": func(token string) error {
			_, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, model.WordPressSearchReplace{Search: change.Search, Replace: "changed.example"}, token)
			return err
		},
		"altered site": func(token string) error {
			_, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-two", target.ID, change, token)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			token := searchReplacePreview(t, state, ctx, owner, "fleet-one", target.ID, change)
			if err := apply(token); err == nil || !strings.Contains(err.Error(), "new search and replace preview") {
				t.Fatalf("bound preview mismatch error = %v", err)
			}
		})
	}
	token := searchReplacePreview(t, state, ctx, owner, "fleet-one", target.ID, change)
	state.now = func() time.Time { return time.Now().UTC().Add(wordpressSearchReplacePreviewTTL + time.Minute) }
	if _, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, change, token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired preview error = %v", err)
	}
}

func TestWordPressSearchReplacePreviewAndApplyRecheckAssignmentAndSiteIdle(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	change := model.WordPressSearchReplace{Search: "old.example", Replace: "new.example"}
	collaborator, err := state.CreateUser(ctx, owner, "search-helper", "a-secure-test-password", rbac.Collaborator, []string{"fleet-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateWordPressSearchReplacePreview(ctx, collaborator, "fleet-two", target.ID, change, broker.WordPressSearchReplaceResult{}); err == nil || err.Error() != "permission denied" {
		t.Fatalf("cross-site preview error = %v", err)
	}
	token := searchReplacePreview(t, state, ctx, collaborator, "fleet-one", target.ID, change)
	if err := state.UpdateUserAccess(ctx, owner, collaborator.ID, rbac.Collaborator, []string{"fleet-two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueWordPressSearchReplace(ctx, collaborator, "fleet-one", target.ID, change, token); err == nil || err.Error() != "permission denied" {
		t.Fatalf("revoked assignment apply error = %v", err)
	}

	ownerToken := searchReplacePreview(t, state, ctx, owner, "fleet-one", target.ID, change)
	if _, err := state.EnqueueWordPressUpdate(ctx, owner, "fleet-one", target.ID, model.WordPressUpdate{Component: model.WordPressPlugin, Name: "akismet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, change, ownerToken); err == nil || !strings.Contains(err.Error(), "operations") {
		t.Fatalf("pending site work did not block search and replace: %v", err)
	}
}

func TestWordPressSearchReplaceFinishRetainsRecoverySnapshotAndRedactsOperands(t *testing.T) {
	const recoveryID = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	for _, test := range []struct {
		name         string
		operationErr error
		wantStatus   string
	}{
		{name: "success", wantStatus: "succeeded"},
		{name: "confirmed failure", operationErr: errors.New("database replacement failed"), wantStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, ctx, owner, target := fleetStoreFixture(t)
			change := model.WordPressSearchReplace{Search: "private-old-value.example", Replace: "private-new-value.example"}
			token := searchReplacePreview(t, state, ctx, owner, "fleet-one", target.ID, change)
			jobID, err := state.EnqueueWordPressSearchReplace(ctx, owner, "fleet-one", target.ID, change, token)
			if err != nil {
				t.Fatal(err)
			}
			job, found, err := state.ClaimNextJob(ctx)
			if err != nil || !found || job.ID != jobID {
				t.Fatalf("claim: job=%#v found=%t err=%v", job, found, err)
			}
			if err := state.FinishJob(ctx, job, `{"tables":0,"replacements":0,"recovery_snapshot_id":"`+recoveryID+`"}`, test.operationErr); err != nil {
				t.Fatal(err)
			}
			finished, err := state.Job(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Status != test.wantStatus || finished.PayloadJSON != "{}" || !strings.Contains(finished.ResultJSON, recoveryID) {
				t.Fatalf("finished job=%#v", finished)
			}
			if test.operationErr != nil && finished.Error != test.operationErr.Error() {
				t.Fatalf("operation error=%q want %q", finished.Error, test.operationErr)
			}
			if strings.Contains(finished.ResultJSON, change.Search) || strings.Contains(finished.ResultJSON, change.Replace) {
				t.Fatalf("private operands leaked into terminal result: %s", finished.ResultJSON)
			}
			snapshots, err := state.ListSiteSnapshots(ctx, "fleet-one")
			if err != nil || len(snapshots) != 1 || snapshots[0].TargetID != target.ID || snapshots[0].ResticSnapshotID != recoveryID {
				t.Fatalf("snapshots=%#v err=%v", snapshots, err)
			}
		})
	}
}
