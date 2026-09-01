package store

import (
	"context"
	"encoding/base32"
	"path/filepath"
	"testing"
	"time"
)

func TestTOTPEnrollmentReplayProtectionAndRecoveryCode(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := state.ConfigureSecretKey(key); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	user, err := state.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := state.BeginTOTPEnrollment(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(secret, now.Unix()/30)
	recovery, err := state.ConfirmTOTPEnrollment(context.Background(), user.ID, code)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery) != 10 {
		t.Fatalf("got %d recovery codes", len(recovery))
	}
	if err := state.VerifySecondFactor(context.Background(), user.ID, code); err != nil {
		t.Fatal(err)
	}
	if err := state.VerifySecondFactor(context.Background(), user.ID, code); err == nil {
		t.Fatal("replayed TOTP was accepted")
	}
	if err := state.VerifySecondFactor(context.Background(), user.ID, recovery[0]); err != nil {
		t.Fatal(err)
	}
	if err := state.VerifySecondFactor(context.Background(), user.ID, recovery[0]); err == nil {
		t.Fatal("replayed recovery code was accepted")
	}
}

func TestLoginChallengeExpires(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	user, err := state.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	// The challenge resolver also requires an enabled factor. Exercise challenge
	// expiry after completing a valid enrollment.
	enrollment, err := state.BeginTOTPEnrollment(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if _, err := state.ConfirmTOTPEnrollment(context.Background(), user.ID, totpCode(secret, now.Unix()/30)); err != nil {
		t.Fatal(err)
	}
	token, err := state.CreateLoginChallenge(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ResolveLoginChallenge(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if _, err := state.ResolveLoginChallenge(context.Background(), token); err == nil {
		t.Fatal("expired login challenge was accepted")
	}
}

func TestDisableTOTPClearsFactorAndRecoveryCodes(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	user, err := state.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := state.BeginTOTPEnrollment(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	recovery, err := state.ConfirmTOTPEnrollment(context.Background(), user.ID, totpCode(secret, now.Unix()/30))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.DisableTOTP(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.VerifySecondFactor(context.Background(), user.ID, recovery[0]); err == nil {
		t.Fatal("recovery code survived TOTP disable")
	}
	resolved, err := state.Authenticate(context.Background(), user.Username, "a-secure-test-password")
	if err != nil || resolved.TOTPEnabled {
		t.Fatalf("user=%#v err=%v", resolved, err)
	}
}
