package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

const OperatorAlertsMigration = `CREATE TABLE operator_alert_settings (
	id INTEGER PRIMARY KEY CHECK(id=1),
	config_ciphertext BLOB NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE operator_alert_state (
	alert_key TEXT PRIMARY KEY,
	active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)),
	bad_samples INTEGER NOT NULL DEFAULT 0,
	good_samples INTEGER NOT NULL DEFAULT 0,
	last_sent_at TEXT,
	last_fingerprint TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);`

type OperatorAlertState struct {
	Key, LastFingerprint string
	Active               bool
	BadSamples           int
	GoodSamples          int
	LastSentAt           time.Time
}

func (s *Store) SaveOperatorAlertSettings(ctx context.Context, actor User, settings model.OperatorAlertSettings) error {
	if actor.Role != rbac.Owner && actor.Role != rbac.Administrator {
		return errors.New("permission denied")
	}
	if err := model.ValidateOperatorAlertSettings(settings); err != nil {
		return err
	}
	settings.Cooldown = 0
	plaintext, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	ciphertext, err := s.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt SMTP settings: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_alert_settings(id,config_ciphertext,updated_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET config_ciphertext=excluded.config_ciphertext,updated_at=excluded.updated_at`, ciphertext, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "operator_alerts.saved", "server", "alerts", "success", fmt.Sprintf(`{"enabled":%t,"auto_swap":%t}`, settings.Enabled, settings.AutoSwap), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OperatorAlertSettings(ctx context.Context) (model.OperatorAlertSettings, error) {
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT config_ciphertext FROM operator_alert_settings WHERE id=1`).Scan(&ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.DefaultOperatorAlertSettings(), nil
		}
		return model.OperatorAlertSettings{}, err
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		return model.OperatorAlertSettings{}, fmt.Errorf("decrypt SMTP settings: %w", err)
	}
	var settings model.OperatorAlertSettings
	if err := json.Unmarshal(plaintext, &settings); err != nil {
		return model.OperatorAlertSettings{}, errors.New("stored alert settings are invalid")
	}
	settings.Cooldown = time.Duration(settings.CooldownMins) * time.Minute
	if err := model.ValidateOperatorAlertSettings(settings); err != nil {
		return model.OperatorAlertSettings{}, errors.New("stored alert settings are invalid")
	}
	return settings, nil
}

func (s *Store) OperatorAlertState(ctx context.Context, key string) (OperatorAlertState, error) {
	if key == "" || len(key) > 512 {
		return OperatorAlertState{}, errors.New("invalid operator alert key")
	}
	var state OperatorAlertState
	var active int
	var last sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT alert_key,active,bad_samples,good_samples,last_sent_at,last_fingerprint FROM operator_alert_state WHERE alert_key=?`, key).Scan(&state.Key, &active, &state.BadSamples, &state.GoodSamples, &last, &state.LastFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		state.Key = key
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Active = active != 0
	if last.Valid {
		state.LastSentAt, _ = time.Parse(time.RFC3339Nano, last.String)
	}
	return state, nil
}

func (s *Store) SaveOperatorAlertState(ctx context.Context, state OperatorAlertState) error {
	if state.Key == "" || len(state.Key) > 512 || state.BadSamples < 0 || state.GoodSamples < 0 || len(state.LastFingerprint) > 128 {
		return errors.New("invalid operator alert state")
	}
	var sent any
	if !state.LastSentAt.IsZero() {
		sent = state.LastSentAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO operator_alert_state(alert_key,active,bad_samples,good_samples,last_sent_at,last_fingerprint,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(alert_key) DO UPDATE SET active=excluded.active,bad_samples=excluded.bad_samples,good_samples=excluded.good_samples,last_sent_at=excluded.last_sent_at,last_fingerprint=excluded.last_fingerprint,updated_at=excluded.updated_at`, state.Key, state.Active, state.BadSamples, state.GoodSamples, sent, state.LastFingerprint, s.now().UTC().Format(time.RFC3339Nano))
	return err
}
