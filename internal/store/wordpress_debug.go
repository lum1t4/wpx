package store

import (
	"context"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

// RecordWordPressDebugEvent records only the action and outcome. Debug output,
// private paths, and host errors never enter the control-plane database.
func (s *Store) RecordWordPressDebugEvent(ctx context.Context, actor User, siteID, action string, succeeded bool) error {
	if err := model.ValidateSiteID(siteID); err != nil {
		return err
	}
	if !s.UserCanSite(ctx, actor, siteID, rbac.ManageWordPress) {
		return errors.New("permission denied")
	}
	switch action {
	case "wordpress.debug_enabled", "wordpress.debug_disabled", "wordpress.debug_log_cleared":
	default:
		return errors.New("invalid WordPress debug audit action")
	}
	result := "failure"
	if succeeded {
		result = "success"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, action, "site", siteID, result, s.now().UTC().Format(time.RFC3339Nano))
	return err
}
