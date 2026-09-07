package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestOperatorAlertSettingsEncryptSecretsAndPersistState(t *testing.T) {
	s := openTestStore(t)
	if err := s.ConfigureSecretKey([]byte(strings.Repeat("k", 32))); err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultOperatorAlertSettings()
	settings.Enabled = true
	settings.SMTP = model.SMTPConfig{Host: "smtp.example.com", Port: 465, Transport: model.SMTPTLS, Username: "user", Password: "mail-secret", From: "alerts@example.com", To: "ops@example.com"}
	if err := s.SaveOperatorAlertSettings(context.Background(), owner, settings); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.db.QueryRow(`SELECT config_ciphertext FROM operator_alert_settings WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mail-secret") {
		t.Fatal("SMTP password stored in plaintext")
	}
	loaded, err := s.OperatorAlertSettings(context.Background())
	if err != nil || loaded.SMTP.Password != "mail-secret" {
		t.Fatalf("load settings: %#v %v", loaded, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	state := OperatorAlertState{Key: "cpu", Active: true, BadSamples: 3, LastSentAt: now, LastFingerprint: "event"}
	if err := s.SaveOperatorAlertState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	got, err := s.OperatorAlertState(context.Background(), "cpu")
	if err != nil || !got.Active || got.BadSamples != 3 || !got.LastSentAt.Equal(now) || got.LastFingerprint != "event" {
		t.Fatalf("state = %#v, %v", got, err)
	}
}
