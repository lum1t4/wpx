package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpdateCheckStatusIsLocalAndRateLimited(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	due, err := state.UpdateCheckDue(ctx, 24*time.Hour)
	if err != nil || !due {
		t.Fatalf("initial due=%v err=%v", due, err)
	}
	if err := state.RecordUpdateCheck(ctx, "v1.0.0", "v1.1.0", "https://github.com/lum1t4/wpx/releases/tag/v1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	due, err = state.UpdateCheckDue(ctx, 24*time.Hour)
	if err != nil || due {
		t.Fatalf("recent due=%v err=%v", due, err)
	}
	if err := state.RecordUpdateCheck(ctx, "v1.0.0", "v1.1.0", "https://github.com/lum1t4/wpx/releases/tag/v1.1.0", errors.New("temporary network failure")); err != nil {
		t.Fatal(err)
	}
	status, err := state.UpdateStatus(ctx)
	if err != nil || status.Status != "failed" || status.LatestVersion != "v1.1.0" || status.Error == "" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}
