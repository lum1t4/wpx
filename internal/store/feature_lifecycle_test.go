package store

import (
	"context"
	"testing"
)

func TestSiteDisableWaitsForFTPAccountChanges(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	activeDatabaseTestSite(t, state, owner)
	_, jobID, err := state.CreateFTPUser(ctx, owner, "database-site", "deployment", "a-secure-ftp-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, "database-site"); err == nil {
		t.Fatal("disable raced queued FTP account changes")
	}
	job, claimed, err := state.ClaimNextJob(ctx)
	if err != nil || !claimed || job.ID != jobID {
		t.Fatalf("claim FTP job: %#v, %v", job, err)
	}
	if _, err := state.EnqueueSiteDelete(ctx, owner, "database-site", "database.example.com"); err == nil {
		t.Fatal("delete raced running FTP account changes")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, "database-site"); err != nil {
		t.Fatalf("completed FTP change left site blocked: %v", err)
	}
}
