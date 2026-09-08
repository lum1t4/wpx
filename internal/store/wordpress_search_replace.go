package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

const WordPressSearchReplacePreviewSchema = `CREATE TABLE wordpress_search_replace_previews (
	token_hash TEXT PRIMARY KEY,
	actor_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	backup_target_id TEXT NOT NULL REFERENCES backup_targets(id) ON DELETE CASCADE,
	change_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	consumed_at TEXT,
	created_at TEXT NOT NULL
)`

const wordpressSearchReplacePreviewTTL = 15 * time.Minute

var storedWordPressTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

func searchReplaceChangeHash(change model.WordPressSearchReplace) (string, error) {
	encoded, err := json.Marshal(change)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateSearchReplaceResult(result broker.WordPressSearchReplaceResult, applying bool) error {
	if result.Tables < 0 || result.Tables > 10000 || result.Replacements < 0 || len(result.TableResults) > 10000 {
		return errors.New("invalid WordPress search and replace result")
	}
	seen := make(map[string]struct{}, len(result.TableResults))
	var total int64
	for _, table := range result.TableResults {
		if !storedWordPressTableNamePattern.MatchString(table.Name) || table.Replacements < 0 {
			return errors.New("invalid WordPress search and replace result")
		}
		if _, duplicate := seen[table.Name]; duplicate {
			return errors.New("invalid WordPress search and replace result")
		}
		seen[table.Name] = struct{}{}
		if table.Replacements > math.MaxInt64-total {
			return errors.New("invalid WordPress search and replace result")
		}
		total += table.Replacements
	}
	if len(result.TableResults) != 0 && (result.Tables != len(result.TableResults) || result.Replacements != total) {
		return errors.New("invalid WordPress search and replace result")
	}
	if applying && !model.ValidResticSnapshotID(result.RecoverySnapshotID) {
		return errors.New("invalid WordPress search and replace recovery result")
	}
	if !applying && result.RecoverySnapshotID != "" {
		return errors.New("invalid WordPress search and replace preview result")
	}
	return nil
}

func authorizeWordPressSearchReplace(ctx context.Context, tx *sql.Tx, actor User, siteID string) error {
	var role rbac.Role
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, actor.ID).Scan(&role, &disabled); err != nil || disabled || !rbac.Allows(role, rbac.ManageWordPress) {
		return errors.New("permission denied")
	}
	if !rbac.Allows(role, rbac.ManageAllSites) {
		var allowed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_grants WHERE user_id=? AND site_id=? AND capability=?`, actor.ID, siteID, rbac.ManageWordPress).Scan(&allowed); err != nil || allowed != 1 {
			return errors.New("permission denied")
		}
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND kind='wordpress' AND status='active'`, siteID).Scan(&active); err != nil || active != 1 {
		return errors.New("search and replace requires an active WordPress site")
	}
	return nil
}

func validateWordPressSearchReplaceContextTx(ctx context.Context, tx *sql.Tx, actor User, siteID, backupTargetID string) error {
	if err := authorizeWordPressSearchReplace(ctx, tx, actor, siteID); err != nil {
		return err
	}
	var ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'`, backupTargetID).Scan(&ready); err != nil || ready != 1 {
		return errors.New("an active backup target is required for recovery")
	}
	return requireSiteIdle(ctx, tx, siteID)
}

// ValidateWordPressSearchReplacePreviewContext checks the same mutable access,
// lifecycle, and recovery preconditions immediately before the broker dry-run.
func (s *Store) ValidateWordPressSearchReplacePreviewContext(ctx context.Context, actor User, siteID, backupTargetID string, change model.WordPressSearchReplace) error {
	if err := model.ValidateSiteID(siteID); err != nil {
		return err
	}
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return validateWordPressSearchReplaceContextTx(ctx, tx, actor, siteID, backupTargetID)
}

func previewTokenHash(token string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid WordPress search and replace preview")
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), nil
}

// CreateWordPressSearchReplacePreview records a successful dry-run and returns
// a one-use token bound to its actor, site, target, and exact literal values.
func (s *Store) CreateWordPressSearchReplacePreview(ctx context.Context, actor User, siteID, backupTargetID string, change model.WordPressSearchReplace, result broker.WordPressSearchReplaceResult) (string, error) {
	if err := validateSearchReplaceResult(result, false); err != nil {
		return "", err
	}
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		return "", err
	}
	changeHash, err := searchReplaceChangeHash(change)
	if err != nil {
		return "", err
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	tokenHash, _ := previewTokenHash(token)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := validateWordPressSearchReplaceContextTx(ctx, tx, actor, siteID, backupTargetID); err != nil {
		return "", err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM wordpress_search_replace_previews WHERE expires_at<=? OR consumed_at IS NOT NULL`, now.Unix())
	if _, err := tx.ExecContext(ctx, `INSERT INTO wordpress_search_replace_previews(token_hash,actor_id,site_id,backup_target_id,change_hash,result_json,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, tokenHash, actor.ID, siteID, backupTargetID, changeHash, string(resultJSON), now.Add(wordpressSearchReplacePreviewTTL).Unix(), now.Format(time.RFC3339Nano)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// EnqueueWordPressSearchReplace consumes the exact successful preview in the
// same transaction that reserves the site with a durable job.
func (s *Store) EnqueueWordPressSearchReplace(ctx context.Context, actor User, siteID, backupTargetID string, change model.WordPressSearchReplace, previewToken string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		return "", err
	}
	tokenHash, err := previewTokenHash(previewToken)
	if err != nil {
		return "", errors.New("run a new search and replace preview")
	}
	changeHash, err := searchReplaceChangeHash(change)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := authorizeWordPressSearchReplace(ctx, tx, actor, siteID); err != nil {
		return "", err
	}
	var ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'`, backupTargetID).Scan(&ready); err != nil || ready != 1 {
		return "", errors.New("an active backup target is required for recovery")
	}
	var expiresAt int64
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM wordpress_search_replace_previews WHERE token_hash=? AND actor_id=? AND site_id=? AND backup_target_id=? AND change_hash=? AND consumed_at IS NULL`, tokenHash, actor.ID, siteID, backupTargetID, changeHash).Scan(&expiresAt); err != nil {
		return "", errors.New("run a new search and replace preview")
	}
	if expiresAt <= now.Unix() {
		return "", errors.New("search and replace preview expired; run a new preview")
	}
	consumed, err := tx.ExecContext(ctx, `UPDATE wordpress_search_replace_previews SET consumed_at=? WHERE token_hash=? AND consumed_at IS NULL`, now.Format(time.RFC3339Nano), tokenHash)
	if err != nil {
		return "", err
	}
	if count, err := consumed.RowsAffected(); err != nil || count != 1 {
		return "", errors.New("run a new search and replace preview")
	}
	if err := requireSiteIdle(ctx, tx, siteID); err != nil {
		return "", err
	}
	jobID := mustID("job_")
	encodedChange, err := json.Marshal(change)
	if err != nil {
		return "", err
	}
	changeCiphertext, err := s.encrypt(encodedChange)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		TargetID         string `json:"target_id"`
		ChangeCiphertext []byte `json:"change_ciphertext"`
	}{TargetID: backupTargetID, ChangeCiphertext: changeCiphertext})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.search_replace", "site", siteID, "queued", "waiting", 0, actor.ID, "wordpress.search_replace:"+siteID+":"+jobID, string(payload), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return "", err
	}
	detail, err := json.Marshal(map[string]any{"change_sha256": changeHash, "search_bytes": len(change.Search), "replacement_bytes": len(change.Replace), "backup_target_id": backupTargetID})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "wordpress.search_replace_requested", "site", siteID, "success", string(detail), now.Format(time.RFC3339Nano)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

// WordPressSearchReplaceJob decrypts the short-lived durable payload only for
// worker dispatch. Generic job summaries never expose PayloadJSON.
func (s *Store) WordPressSearchReplaceJob(job Job) (string, model.WordPressSearchReplace, error) {
	if job.Kind != "wordpress.search_replace" {
		return "", model.WordPressSearchReplace{}, errors.New("invalid WordPress search and replace job")
	}
	var payload struct {
		TargetID         string `json:"target_id"`
		ChangeCiphertext []byte `json:"change_ciphertext"`
	}
	decoder := json.NewDecoder(strings.NewReader(job.PayloadJSON))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decodeHasTrailingJSON(decoder) || payload.TargetID == "" || len(payload.ChangeCiphertext) == 0 {
		return "", model.WordPressSearchReplace{}, errors.New("invalid WordPress search and replace job payload")
	}
	plaintext, err := s.decrypt(payload.ChangeCiphertext)
	if err != nil {
		return "", model.WordPressSearchReplace{}, errors.New("decrypt WordPress search and replace job payload")
	}
	var change model.WordPressSearchReplace
	changeDecoder := json.NewDecoder(strings.NewReader(string(plaintext)))
	changeDecoder.DisallowUnknownFields()
	if changeDecoder.Decode(&change) != nil || decodeHasTrailingJSON(changeDecoder) || model.ValidateWordPressSearchReplace(change) != nil {
		return "", model.WordPressSearchReplace{}, errors.New("invalid WordPress search and replace job payload")
	}
	return payload.TargetID, change, nil
}

// RetryWordPressSearchReplaceJob preserves the encrypted payload and durable
// idempotency key while a transport-uncertain host result is reconciled.
func (s *Store) RetryWordPressSearchReplaceJob(ctx context.Context, jobID, detail string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind='wordpress.search_replace' AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("WordPress search and replace job is not awaiting broker confirmation")
	}
	return nil
}

func decodeHasTrailingJSON(decoder *json.Decoder) bool {
	var trailing any
	return !errors.Is(decoder.Decode(&trailing), io.EOF)
}
