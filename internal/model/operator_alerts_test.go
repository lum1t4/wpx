package model

import "testing"

func TestOperatorAlertSettingsRejectsUnsafeSMTPAndThresholds(t *testing.T) {
	valid := DefaultOperatorAlertSettings()
	valid.Enabled = true
	valid.SMTP = SMTPConfig{Host: "smtp.example.com", Port: 587, Transport: SMTPSTARTTLS, Username: "operator", Password: "secret", From: "alerts@example.com", To: "ops@example.com"}
	if err := ValidateOperatorAlertSettings(valid); err != nil {
		t.Fatalf("valid settings: %v", err)
	}
	for name, mutate := range map[string]func(*OperatorAlertSettings){
		"header injection": func(s *OperatorAlertSettings) { s.SMTP.To = "ops@example.com\r\nBcc: x@example.com" },
		"url host":         func(s *OperatorAlertSettings) { s.SMTP.Host = "https://smtp.example.com" },
		"plain transport":  func(s *OperatorAlertSettings) { s.SMTP.Transport = "plain" },
		"low threshold":    func(s *OperatorAlertSettings) { s.MemoryPercent = 20 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if ValidateOperatorAlertSettings(candidate) == nil {
				t.Fatal("unsafe settings accepted")
			}
		})
	}
}

func TestPublicOperatorAlertSettingsRedactsPassword(t *testing.T) {
	settings := DefaultOperatorAlertSettings()
	settings.SMTP.Password = "secret"
	settings.Slack.WebhookURL = "https://hooks.slack.com/services/T/B/secret"
	settings.Telegram.BotToken = "123456:abcdefghijklmnopqrstuvwxyzABCDEFGHI"
	if got := PublicOperatorAlertSettings(settings); got.SMTP.Password != "" || got.Slack.WebhookURL != "" || got.Telegram.BotToken != "" || settings.SMTP.Password != "secret" {
		t.Fatalf("redaction mutated input or exposed password: %#v", got.SMTP)
	}
}

func TestOperatorAlertChannelsValidateIndependently(t *testing.T) {
	if err := ValidateOperatorAlertSettings(DefaultOperatorAlertSettings()); err != nil {
		t.Fatalf("disabled defaults with blank SMTP must be savable: %v", err)
	}
	settings := DefaultOperatorAlertSettings()
	settings.SMTPEnabled = false
	settings.Enabled = true
	if err := ValidateOperatorAlertSettings(settings); err == nil {
		t.Fatal("enabled notifications accepted without a channel")
	}
	settings.Slack = SlackAlertConfig{Enabled: true, WebhookURL: "https://hooks.slack.com/services/T000/B000/secret_token"}
	if err := ValidateOperatorAlertSettings(settings); err != nil {
		t.Fatalf("valid Slack channel: %v", err)
	}
	settings.Slack.WebhookURL = "https://example.com/services/T000/B000/secret_token"
	if err := ValidateOperatorAlertSettings(settings); err == nil {
		t.Fatal("untrusted Slack endpoint accepted")
	}
	settings.Slack.Enabled = false
	settings.Telegram = TelegramAlertConfig{Enabled: true, BotToken: "123456789:abcdefghijklmnopqrstuvwxyzABCDEFGHI", ChatID: "-100123456789"}
	if err := ValidateOperatorAlertSettings(settings); err != nil {
		t.Fatalf("valid Telegram channel: %v", err)
	}
	settings.Telegram.ChatID = "chat id"
	if err := ValidateOperatorAlertSettings(settings); err == nil {
		t.Fatal("invalid Telegram chat ID accepted")
	}
}

func TestOperatorAlertRuleSelectionSupportsLegacyAndExplicitEmptyMasks(t *testing.T) {
	settings := DefaultOperatorAlertSettings()
	for _, rule := range []AlertRule{AlertCPU, AlertMemory, AlertDisk, AlertServices, AlertSSLExpiry, AlertUpdates, AlertOOM} {
		if !settings.RulesForChannel("smtp").Allows(rule) {
			t.Errorf("legacy SMTP mask did not inherit %s", rule)
		}
	}
	settings.SMTPRules = &AlertRuleSelection{}
	if settings.RulesForChannel("smtp").Allows(AlertCPU) {
		t.Fatal("explicit empty SMTP mask inherited global CPU unexpectedly")
	}
	settings.Slack.Rules = &AlertRuleSelection{Disk: true}
	if !settings.RulesForChannel("slack").Allows(AlertDisk) || settings.RulesForChannel("slack").Allows(AlertCPU) {
		t.Fatal("explicit Slack mask was not applied independently")
	}
}
