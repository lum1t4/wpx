package store

import (
	"context"
	"encoding/json"
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
	settings.Slack = model.SlackAlertConfig{Enabled: true, WebhookURL: "https://hooks.slack.com/services/T000/B000/slack-secret"}
	settings.Telegram = model.TelegramAlertConfig{Enabled: true, BotToken: "123456789:abcdefghijklmnopqrstuvwxyzABCDEFGHI", ChatID: "-100123456789"}
	if err := s.SaveOperatorAlertSettings(context.Background(), owner, settings); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.db.QueryRow(`SELECT config_ciphertext FROM operator_alert_settings WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mail-secret") || strings.Contains(string(raw), "slack-secret") || strings.Contains(string(raw), "abcdefghijklmnopqrstuvwxyzABCDEFGHI") {
		t.Fatal("alert channel credential stored in plaintext")
	}
	loaded, err := s.OperatorAlertSettings(context.Background())
	if err != nil || loaded.SMTP.Password != "mail-secret" || loaded.Slack.WebhookURL != settings.Slack.WebhookURL || loaded.Telegram.BotToken != settings.Telegram.BotToken {
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

func TestOperatorAlertSettingsLegacyJSONKeepsSMTPEnabled(t *testing.T) {
	s := openTestStore(t)
	if err := s.ConfigureSecretKey([]byte(strings.Repeat("l", 32))); err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultOperatorAlertSettings()
	settings.SMTP = model.SMTPConfig{Host: "smtp.example.com", Port: 587, Transport: model.SMTPSTARTTLS, From: "alerts@example.com", To: "ops@example.com"}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "smtp_enabled")
	raw, _ = json.Marshal(legacy)
	ciphertext, err := s.encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO operator_alert_settings(id,config_ciphertext,updated_at) VALUES(1,?,?)`, ciphertext, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.OperatorAlertSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.SMTPEnabled {
		t.Fatal("legacy SMTP channel was disabled")
	}
	if loaded.SMTPRules != nil || !loaded.RulesForChannel("smtp").CPU {
		t.Fatalf("legacy SMTP rules did not inherit global rules: %#v", loaded.SMTPRules)
	}
}

func TestOperatorAlertSettingsPersistsDisabledSMTPAndExplicitEmptyRules(t *testing.T) {
	s := openTestStore(t)
	if err := s.ConfigureSecretKey([]byte(strings.Repeat("m", 32))); err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultOperatorAlertSettings()
	settings.SMTPEnabled = false
	settings.SMTPRules = &model.AlertRuleSelection{}
	settings.Slack.Rules = &model.AlertRuleSelection{CPU: true}
	if err := s.SaveOperatorAlertSettings(context.Background(), owner, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.OperatorAlertSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SMTPEnabled || loaded.SMTPRules == nil || loaded.SMTPRules.CPU || loaded.Slack.Rules == nil || !loaded.Slack.Rules.CPU {
		t.Fatalf("channel settings did not round-trip: %#v", loaded)
	}
	var ciphertext []byte
	if err := s.db.QueryRow(`SELECT config_ciphertext FROM operator_alert_settings WHERE id=1`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plaintext), `"smtp_enabled":false`) || !strings.Contains(string(plaintext), `"smtp_rules":{`) {
		t.Fatalf("explicit false/empty mask absent from stored JSON: %s", plaintext)
	}
}
