package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod     = 30 * time.Second
	loginChallenge = 5 * time.Minute
)

type TOTPEnrollment struct {
	Secret string
	URI    string
}

// PendingTOTPEnrollment lets a user return to setup after a page refresh or an
// invalid confirmation code without changing the key already in their app.
// Only pending secrets are readable: the database decides whether a factor is
// enabled, since the caller's User may predate a successful confirmation.
func (s *Store) PendingTOTPEnrollment(ctx context.Context, user User) (TOTPEnrollment, error) {
	var username string
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT username,
		CASE WHEN totp_secret_ciphertext IS NULL THEN totp_pending_ciphertext END
		FROM users WHERE id=?`, user.ID).Scan(&username, &ciphertext); err != nil {
		return TOTPEnrollment{}, fmt.Errorf("read pending TOTP enrollment: %w", err)
	}
	if len(ciphertext) == 0 {
		return TOTPEnrollment{}, nil
	}
	secret, err := s.decrypt(ciphertext)
	if err != nil {
		return TOTPEnrollment{}, fmt.Errorf("decrypt pending TOTP enrollment: %w", err)
	}
	return totpEnrollment(username, string(secret)), nil
}

func (s *Store) BeginTOTPEnrollment(ctx context.Context, user User) (TOTPEnrollment, error) {
	var alreadyEnabled bool
	if err := s.db.QueryRowContext(ctx, "SELECT totp_secret_ciphertext IS NOT NULL FROM users WHERE id=?", user.ID).Scan(&alreadyEnabled); err != nil {
		return TOTPEnrollment{}, err
	}
	if alreadyEnabled {
		return TOTPEnrollment{}, errors.New("TOTP is already enabled")
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return TOTPEnrollment{}, err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	ciphertext, err := s.encrypt([]byte(encoded))
	if err != nil {
		return TOTPEnrollment{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE users SET totp_pending_ciphertext=?,updated_at=? WHERE id=?", ciphertext, s.now().UTC().Format(time.RFC3339Nano), user.ID); err != nil {
		return TOTPEnrollment{}, fmt.Errorf("store pending TOTP enrollment: %w", err)
	}
	return totpEnrollment(user.Username, encoded), nil
}

func totpEnrollment(username, secret string) TOTPEnrollment {
	const issuer = "WPX"
	uri := "otpauth://totp/" + url.PathEscape(issuer+":"+username) + "?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
	return TOTPEnrollment{Secret: secret, URI: uri}
}

func (s *Store) ConfirmTOTPEnrollment(ctx context.Context, userID, code string) ([]string, error) {
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, "SELECT totp_pending_ciphertext FROM users WHERE id=?", userID).Scan(&ciphertext); err != nil || len(ciphertext) == 0 {
		return nil, errors.New("no pending TOTP enrollment")
	}
	secret, err := s.decrypt(ciphertext)
	if err != nil {
		return nil, err
	}
	if _, ok := validTOTPCounter(string(secret), normalizeCode(code), s.now()); !ok {
		return nil, errors.New("the authenticator code is invalid")
	}
	codes, hashes, err := s.newRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret_ciphertext=totp_pending_ciphertext,
		totp_pending_ciphertext=NULL,totp_last_counter=-1,updated_at=? WHERE id=?`, now, userID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM totp_recovery_codes WHERE user_id=?", userID); err != nil {
		return nil, err
	}
	for _, hash := range hashes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO totp_recovery_codes(user_id,code_hash,created_at) VALUES(?,?,?)", userID, hash, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), userID, "totp.enabled", "user", userID, "success", now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret_ciphertext=NULL,
		totp_pending_ciphertext=NULL,totp_last_counter=-1,updated_at=?
		WHERE id=? AND totp_secret_ciphertext IS NOT NULL`, now, userID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("TOTP is not enabled")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), userID, "totp.disabled", "user", userID, "success", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) VerifySecondFactor(ctx context.Context, userID, code string) error {
	normalized := normalizeCode(code)
	var ciphertext []byte
	var lastCounter int64
	if err := s.db.QueryRowContext(ctx, "SELECT totp_secret_ciphertext,totp_last_counter FROM users WHERE id=?", userID).Scan(&ciphertext, &lastCounter); err != nil || len(ciphertext) == 0 {
		return errors.New("second factor is unavailable")
	}
	secret, err := s.decrypt(ciphertext)
	if err != nil {
		return err
	}
	if counter, ok := validTOTPCounter(string(secret), normalized, s.now()); ok && counter > lastCounter {
		result, err := s.db.ExecContext(ctx, "UPDATE users SET totp_last_counter=? WHERE id=? AND totp_last_counter<?", counter, userID, counter)
		if err == nil {
			changed, _ := result.RowsAffected()
			if changed == 1 {
				return nil
			}
		}
	}
	return s.consumeRecoveryCode(ctx, userID, normalized)
}

func (s *Store) CreateLoginChallenge(ctx context.Context, userID string) (string, error) {
	token, err := randomID("mfa_", 32)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO login_challenges(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		digest[:], userID, now.Format(time.RFC3339Nano), now.Add(loginChallenge).Format(time.RFC3339Nano))
	return token, err
}

func (s *Store) ResolveLoginChallenge(ctx context.Context, token string) (User, error) {
	digest := sha256.Sum256([]byte(token))
	var user User
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.username,u.role,u.disabled,u.totp_secret_ciphertext IS NOT NULL,c.expires_at
		FROM login_challenges c JOIN users u ON u.id=c.user_id WHERE c.token_hash=?`, digest[:]).
		Scan(&user.ID, &user.Username, &user.Role, &user.Disabled, &user.TOTPEnabled, &expires)
	if err != nil || user.Disabled || !user.TOTPEnabled {
		return User{}, errors.New("invalid login challenge")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !s.now().Before(expiresAt) {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM login_challenges WHERE token_hash=?", digest[:])
		return User{}, errors.New("invalid login challenge")
	}
	return user, nil
}

func (s *Store) DeleteLoginChallenge(ctx context.Context, token string) error {
	digest := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, "DELETE FROM login_challenges WHERE token_hash=?", digest[:])
	return err
}

func (s *Store) encrypt(plaintext []byte) ([]byte, error) {
	if len(s.secretKey) != 32 {
		return nil, errors.New("application secret key is not configured")
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *Store) decrypt(ciphertext []byte) ([]byte, error) {
	if len(s.secretKey) != 32 {
		return nil, errors.New("application secret key is not configured")
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("encrypted value is truncated")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
}

func validTOTPCounter(secret, code string, now time.Time) (int64, bool) {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil || len(code) != 6 {
		return 0, false
	}
	current := now.Unix() / int64(totpPeriod/time.Second)
	for offset := int64(-1); offset <= 1; offset++ {
		counter := current + offset
		if subtle.ConstantTimeCompare([]byte(totpCode(decoded, counter)), []byte(code)) == 1 {
			return counter, true
		}
	}
	return 0, false
}

func totpCode(secret []byte, counter int64) string {
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, uint64(counter))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(message)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
}

func (s *Store) newRecoveryCodes(count int) ([]string, [][]byte, error) {
	if len(s.secretKey) != 32 {
		return nil, nil, errors.New("application secret key is not configured")
	}
	codes := make([]string, 0, count)
	hashes := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, nil, err
		}
		raw := strings.ToUpper(hex.EncodeToString(random))
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
		codes = append(codes, code)
		hashes = append(hashes, s.recoveryHash(normalizeCode(code)))
	}
	return codes, hashes, nil
}

func (s *Store) recoveryHash(code string) []byte {
	mac := hmac.New(sha256.New, s.secretKey)
	_, _ = mac.Write([]byte("wpx-recovery:" + code))
	return mac.Sum(nil)
}

func (s *Store) consumeRecoveryCode(ctx context.Context, userID, code string) error {
	if len(code) != 16 {
		return errors.New("invalid second factor")
	}
	hash := s.recoveryHash(code)
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE totp_recovery_codes SET used_at=? WHERE user_id=? AND code_hash=? AND used_at IS NULL`, now, userID, hash)
	if err != nil {
		return errors.New("invalid second factor")
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("invalid second factor")
	}
	return nil
}
