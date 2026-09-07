package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lum1t4/wpx/internal/rbac"
)

// JobForUser returns the browser-safe lifecycle fields for a job the user may
// follow. The initiator keeps access after a successful delete removes the
// target row; administrators may follow all jobs. Other users do not gain
// access merely by learning a job identifier.
func (s *Store) JobForUser(ctx context.Context, user User, id string) (Job, error) {
	var job Job
	var initiatorID string
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,target_type,target_id,status,phase,progress,error,initiator_id
		FROM jobs WHERE id=?`, id).Scan(&job.ID, &job.Kind, &job.TargetType, &job.TargetID, &job.Status, &job.Phase, &job.Progress, &job.Error, &initiatorID)
	if err != nil {
		return Job{}, err
	}
	if rbac.Allows(user.Role, rbac.ManageAllSites) || initiatorID == user.ID {
		return job, nil
	}
	if job.TargetType == "site" && s.UserCanSite(ctx, user, job.TargetID, rbac.ViewSite) {
		return job, nil
	}
	return Job{}, sql.ErrNoRows
}

func IsJobNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
