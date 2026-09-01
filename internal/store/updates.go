package store

import (
	"context"
	"database/sql"
	"time"
)

type UpdateStatus struct {
	CurrentVersion string
	LatestVersion  string
	ReleaseURL     string
	Status         string
	Error          string
	CheckedAt      string
}

func (s *Store) UpdateStatus(ctx context.Context) (UpdateStatus, error) {
	var status UpdateStatus
	err := s.db.QueryRowContext(ctx, `SELECT current_version,latest_version,release_url,status,error,checked_at FROM update_status WHERE id=1`).
		Scan(&status.CurrentVersion, &status.LatestVersion, &status.ReleaseURL, &status.Status, &status.Error, &status.CheckedAt)
	if err == sql.ErrNoRows {
		return UpdateStatus{}, nil
	}
	return status, err
}

func (s *Store) UpdateCheckDue(ctx context.Context, interval time.Duration) (bool, error) {
	status, err := s.UpdateStatus(ctx)
	if err != nil || status.CheckedAt == "" {
		return err == nil, err
	}
	checked, err := time.Parse(time.RFC3339Nano, status.CheckedAt)
	if err != nil {
		return true, nil
	}
	return !checked.Add(interval).After(s.now().UTC()), nil
}

func (s *Store) RecordUpdateCheck(ctx context.Context, current, latest, releaseURL string, checkErr error) error {
	status, errorText := "ok", ""
	if checkErr != nil {
		status, errorText = "failed", checkErr.Error()
		if len(errorText) > 300 {
			errorText = errorText[:300]
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO update_status(id,current_version,latest_version,release_url,status,error,checked_at) VALUES(1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET current_version=excluded.current_version,latest_version=excluded.latest_version,release_url=excluded.release_url,status=excluded.status,error=excluded.error,checked_at=excluded.checked_at`, current, latest, releaseURL, status, errorText, now)
	return err
}
