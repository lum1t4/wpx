package model

import "testing"

func TestOperatorAlertSettingsRejectsUnsafeSMTPAndThresholds(t *testing.T) {
	valid := DefaultOperatorAlertSettings()
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
	if got := PublicOperatorAlertSettings(settings); got.SMTP.Password != "" || settings.SMTP.Password != "secret" {
		t.Fatalf("redaction mutated input or exposed password: %#v", got.SMTP)
	}
}
