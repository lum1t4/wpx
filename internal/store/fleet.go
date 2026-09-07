package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

const maxFleetPluginUpdates = 200

// FleetPluginUpdate identifies one installed plugin on one site. The update
// itself still runs through the ordinary wordpress.update worker operation, so
// every job gets the same recovery backup and health-check boundary as an
// update requested from the individual site page.
type FleetPluginUpdate struct {
	SiteID string
	Plugin string
}

type FleetUpdateJob struct {
	SiteID string
	Plugin string
	JobID  string
}

// EnqueueFleetPluginUpdates validates the entire batch before inserting any
// jobs. This keeps an unauthorized, inactive, or malformed target from leaving
// an apparently successful partial batch behind.
func (s *Store) EnqueueFleetPluginUpdates(ctx context.Context, actor User, backupTargetID string, updates []FleetPluginUpdate) ([]FleetUpdateJob, error) {
	if len(updates) == 0 {
		return nil, errors.New("select at least one plugin update")
	}
	if len(updates) > maxFleetPluginUpdates {
		return nil, errors.New("too many plugin updates selected")
	}
	seen := make(map[string]struct{}, len(updates))
	for _, selected := range updates {
		if err := model.ValidateSiteID(selected.SiteID); err != nil {
			return nil, errors.New("invalid plugin update selection")
		}
		if err := model.ValidateWordPressUpdate(model.WordPressUpdate{Component: model.WordPressPlugin, Name: selected.Plugin}); err != nil {
			return nil, err
		}
		key := selected.SiteID + "\x00" + selected.Plugin
		if _, exists := seen[key]; exists {
			return nil, errors.New("duplicate plugin update selection")
		}
		seen[key] = struct{}{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var actorRole rbac.Role
	var actorDisabled bool
	if err := tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, actor.ID).Scan(&actorRole, &actorDisabled); err != nil || actorDisabled || !rbac.Allows(actorRole, rbac.ManageWordPress) {
		return nil, errors.New("permission denied")
	}
	var ready int
	checkedSites := make(map[string]struct{}, len(updates))
	for _, selected := range updates {
		if _, checked := checkedSites[selected.SiteID]; checked {
			continue
		}
		if !rbac.Allows(actorRole, rbac.ManageAllSites) {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_grants WHERE user_id=? AND site_id=? AND capability=?`, actor.ID, selected.SiteID, rbac.ManageWordPress).Scan(&ready); err != nil || ready != 1 {
				return nil, errors.New("permission denied")
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND kind='wordpress' AND status='active'`, selected.SiteID).Scan(&ready); err != nil || ready != 1 {
			return nil, fmt.Errorf("site %s is not an active WordPress site", selected.SiteID)
		}
		// Multiple selections for this same site belong to this transaction and
		// run serially in the worker. Work already queued before the batch could
		// mutate the site between its backup and update, so reject the batch.
		if err := requireSiteIdle(ctx, tx, selected.SiteID); err != nil {
			return nil, err
		}
		checkedSites[selected.SiteID] = struct{}{}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'`, backupTargetID).Scan(&ready); err != nil || ready != 1 {
		return nil, errors.New("an active backup target is required for update recovery")
	}

	now := s.now().UTC().Format(time.RFC3339Nano)
	jobs := make([]FleetUpdateJob, 0, len(updates))
	for _, selected := range updates {
		jobID := mustID("job_")
		update := model.WordPressUpdate{Component: model.WordPressPlugin, Name: selected.Plugin}
		payload, err := json.Marshal(struct {
			TargetID string                `json:"target_id"`
			Update   model.WordPressUpdate `json:"update"`
		}{TargetID: backupTargetID, Update: update})
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.update", "site", selected.SiteID, "queued", "waiting", 0, actor.ID, "wordpress.update:"+selected.SiteID+":"+jobID, string(payload), now, now); err != nil {
			return nil, err
		}
		detail, err := json.Marshal(map[string]string{"component": string(model.WordPressPlugin), "name": selected.Plugin, "backup_target_id": backupTargetID, "source": "fleet"})
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "wordpress.update_requested", "site", selected.SiteID, "success", string(detail), now); err != nil {
			return nil, err
		}
		jobs = append(jobs, FleetUpdateJob{SiteID: selected.SiteID, Plugin: selected.Plugin, JobID: jobID})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}
