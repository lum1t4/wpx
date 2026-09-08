package model

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type SMTPTransport string

const (
	SMTPSTARTTLS SMTPTransport = "starttls"
	SMTPTLS      SMTPTransport = "tls"
)

type OperatorAlertSettings struct {
	Enabled       bool                `json:"enabled"`
	CPU           bool                `json:"cpu"`
	Memory        bool                `json:"memory"`
	Disk          bool                `json:"disk"`
	Services      bool                `json:"services"`
	SSLExpiry     bool                `json:"ssl_expiry"`
	Updates       bool                `json:"updates"`
	OOM           bool                `json:"oom"`
	AutoSwap      bool                `json:"auto_swap"`
	CPUPercent    int                 `json:"cpu_percent"`
	MemoryPercent int                 `json:"memory_percent"`
	DiskPercent   int                 `json:"disk_percent"`
	SSLExpiryDays int                 `json:"ssl_expiry_days"`
	Cooldown      time.Duration       `json:"-"`
	CooldownMins  int                 `json:"cooldown_minutes"`
	SMTPEnabled   bool                `json:"smtp_enabled"`
	SMTP          SMTPConfig          `json:"smtp"`
	Slack         SlackAlertConfig    `json:"slack"`
	Telegram      TelegramAlertConfig `json:"telegram"`
}

type SlackAlertConfig struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

type TelegramAlertConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

type SMTPConfig struct {
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	Transport SMTPTransport `json:"transport"`
	Username  string        `json:"username,omitempty"`
	Password  string        `json:"password,omitempty"`
	From      string        `json:"from"`
	To        string        `json:"to"`
}

func DefaultOperatorAlertSettings() OperatorAlertSettings {
	return OperatorAlertSettings{CPU: true, Memory: true, Disk: true, Services: true, SSLExpiry: true, Updates: true, OOM: true,
		CPUPercent: 90, MemoryPercent: 90, DiskPercent: 90, SSLExpiryDays: 14, CooldownMins: 60, Cooldown: time.Hour,
		SMTPEnabled: true, SMTP: SMTPConfig{Port: 587, Transport: SMTPSTARTTLS}}
}

var smtpHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var telegramTokenPattern = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{30,80}$`)
var telegramChatPattern = regexp.MustCompile(`^(?:-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

func ValidateOperatorAlertSettings(settings OperatorAlertSettings) error {
	if settings.CPUPercent < 50 || settings.CPUPercent > 100 || settings.MemoryPercent < 50 || settings.MemoryPercent > 100 || settings.DiskPercent < 50 || settings.DiskPercent > 100 {
		return errors.New("resource thresholds must be between 50 and 100 percent")
	}
	if settings.SSLExpiryDays < 1 || settings.SSLExpiryDays > 90 {
		return errors.New("certificate warning must be between 1 and 90 days")
	}
	if settings.CooldownMins < 15 || settings.CooldownMins > 10080 {
		return errors.New("cooldown must be between 15 minutes and 7 days")
	}
	settings.Cooldown = time.Duration(settings.CooldownMins) * time.Minute
	if len(settings.SMTP.Password) > 1024 || len(settings.Slack.WebhookURL) > 2048 || len(settings.Telegram.BotToken) > 128 || len(settings.Telegram.ChatID) > 64 {
		return errors.New("alert channel credentials are too long")
	}
	if strings.ContainsAny(settings.Slack.WebhookURL+settings.Telegram.BotToken+settings.Telegram.ChatID, "\r\n") {
		return errors.New("alert channel credentials are invalid")
	}
	if settings.Enabled && !settings.SMTPEnabled && !settings.Slack.Enabled && !settings.Telegram.Enabled {
		return errors.New("enable at least one alert delivery channel")
	}
	if settings.Enabled && settings.SMTPEnabled {
		if err := ValidateSMTPConfig(settings.SMTP); err != nil {
			return err
		}
	}
	if settings.Enabled && settings.Slack.Enabled {
		if err := ValidateSlackAlertConfig(settings.Slack); err != nil {
			return err
		}
	}
	if settings.Enabled && settings.Telegram.Enabled {
		if err := ValidateTelegramAlertConfig(settings.Telegram); err != nil {
			return err
		}
	}
	return nil
}

func ValidateSlackAlertConfig(config SlackAlertConfig) error {
	raw := strings.TrimSpace(config.WebhookURL)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n") {
		return errors.New("Slack webhook URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Slack webhook must use the official hooks.slack.com address")
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] != "services" {
		return errors.New("Slack webhook path is invalid")
	}
	for _, part := range parts[1:] {
		if part == "" || len(part) > 128 || strings.Contains(part, "%") {
			return errors.New("Slack webhook path is invalid")
		}
	}
	return nil
}

func ValidateTelegramAlertConfig(config TelegramAlertConfig) error {
	if !telegramTokenPattern.MatchString(strings.TrimSpace(config.BotToken)) {
		return errors.New("Telegram bot token is invalid")
	}
	if !telegramChatPattern.MatchString(strings.TrimSpace(config.ChatID)) {
		return errors.New("Telegram chat ID is invalid")
	}
	return nil
}

func ValidateSMTPConfig(config SMTPConfig) error {
	host := strings.TrimSpace(config.Host)
	ip := net.ParseIP(host)
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "\r\n/") || (ip == nil && (strings.Contains(host, ":") || !smtpHostPattern.MatchString(host))) {
		return errors.New("SMTP host must be a hostname or IP address without a scheme")
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("SMTP port is invalid")
	}
	if config.Transport != SMTPSTARTTLS && config.Transport != SMTPTLS {
		return errors.New("SMTP transport must be STARTTLS or TLS")
	}
	if len(config.Username) > 320 || len(config.Password) > 1024 || strings.ContainsAny(config.Username, "\r\n") {
		return errors.New("SMTP credentials are invalid")
	}
	for label, value := range map[string]string{"sender": config.From, "recipient": config.To} {
		address, err := mail.ParseAddress(strings.TrimSpace(value))
		if err != nil || address.Address != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("SMTP %s must be one email address", label)
		}
	}
	return nil
}

func PublicOperatorAlertSettings(settings OperatorAlertSettings) OperatorAlertSettings {
	settings.SMTP.Password = ""
	settings.Slack.WebhookURL = ""
	settings.Telegram.BotToken = ""
	return settings
}
