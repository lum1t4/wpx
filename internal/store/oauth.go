package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

const googleOAuthFlowLifetime = 10 * time.Minute

type GoogleDriveOAuthFlow struct {
	Actor       User
	Target      model.BackupTarget
	RedirectURI string
	Verifier    string
}

type storedGoogleDriveOAuthFlow struct {
	Target   model.BackupTarget `json:"target"`
	Verifier string             `json:"verifier"`
}

func (s *Store) CreateGoogleDriveOAuthFlow(ctx context.Context, actor User, target model.BackupTarget, redirectURI string) (string, string, error) {
	if actor.Disabled {
		return "", "", errors.New("account is disabled")
	}
	callback, err := url.Parse(redirectURI)
	if err != nil || callback.Scheme != "https" || callback.Host == "" || callback.User != nil || callback.Path != "/backups/google-drive/callback" || callback.RawQuery != "" || callback.Fragment != "" {
		return "", "", errors.New("Google OAuth callback must be the panel HTTPS callback URL")
	}
	target.Kind = model.BackupGoogleDrive
	target.Name = strings.TrimSpace(target.Name)
	target.DriveFolder = strings.Trim(strings.TrimSpace(target.DriveFolder), "/")
	target.GoogleClientID = strings.TrimSpace(target.GoogleClientID)
	target.GoogleClientSecret = strings.TrimSpace(target.GoogleClientSecret)
	target.GoogleSharedDrive = strings.TrimSpace(target.GoogleSharedDrive)
	if err := model.ValidateGoogleDriveSetup(target); err != nil {
		return "", "", err
	}
	stateBytes := make([]byte, 32)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	encoded, err := json.Marshal(storedGoogleDriveOAuthFlow{Target: target, Verifier: verifier})
	if err != nil {
		return "", "", err
	}
	ciphertext, err := s.encrypt(encoded)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256([]byte(state))
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_flows WHERE expires_at<=?`, now.Format(time.RFC3339Nano)); err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_flows(state_hash,user_id,kind,config_ciphertext,redirect_uri,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?)`, hash[:], actor.ID, "google_drive", ciphertext, redirectURI, now.Format(time.RFC3339Nano), now.Add(googleOAuthFlowLifetime).Format(time.RFC3339Nano))
	if err != nil {
		return "", "", err
	}
	return state, verifier, nil
}

// ConsumeGoogleDriveOAuthFlow removes the state in the same transaction that
// reads it. Google authorization codes and WPX state values are both one-time.
func (s *Store) ConsumeGoogleDriveOAuthFlow(ctx context.Context, state string) (GoogleDriveOAuthFlow, error) {
	if len(state) < 40 || len(state) > 128 {
		return GoogleDriveOAuthFlow{}, errors.New("Google authorization state is invalid")
	}
	hash := sha256.Sum256([]byte(state))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GoogleDriveOAuthFlow{}, err
	}
	defer tx.Rollback()
	var userID, kind, redirectURI, expiresAt string
	var ciphertext []byte
	err = tx.QueryRowContext(ctx, `SELECT user_id,kind,config_ciphertext,redirect_uri,expires_at FROM oauth_flows WHERE state_hash=?`, hash[:]).Scan(&userID, &kind, &ciphertext, &redirectURI, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GoogleDriveOAuthFlow{}, errors.New("Google authorization has expired or was already used")
	}
	if err != nil {
		return GoogleDriveOAuthFlow{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_flows WHERE state_hash=?`, hash[:]); err != nil {
		return GoogleDriveOAuthFlow{}, err
	}
	if err := tx.Commit(); err != nil {
		return GoogleDriveOAuthFlow{}, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !s.now().UTC().Before(expiry) || kind != "google_drive" {
		return GoogleDriveOAuthFlow{}, errors.New("Google authorization has expired or is invalid")
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		return GoogleDriveOAuthFlow{}, err
	}
	var stored storedGoogleDriveOAuthFlow
	if err := json.Unmarshal(plaintext, &stored); err != nil || stored.Verifier == "" {
		return GoogleDriveOAuthFlow{}, errors.New("stored Google authorization is invalid")
	}
	actor, err := s.User(ctx, userID)
	if err != nil || actor.Disabled {
		return GoogleDriveOAuthFlow{}, errors.New("the account that started Google authorization is unavailable")
	}
	if err := model.ValidateGoogleDriveSetup(stored.Target); err != nil {
		return GoogleDriveOAuthFlow{}, errors.New("stored Google Drive setup is invalid")
	}
	return GoogleDriveOAuthFlow{Actor: actor, Target: stored.Target, RedirectURI: redirectURI, Verifier: stored.Verifier}, nil
}
