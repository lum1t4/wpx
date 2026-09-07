package model

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
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
	Enabled       bool          `json:"enabled"`
	CPU           bool          `json:"cpu"`
	Memory        bool          `json:"memory"`
	Disk          bool          `json:"disk"`
	Services      bool          `json:"services"`
	SSLExpiry     bool          `json:"ssl_expiry"`
	Updates       bool          `json:"updates"`
	OOM           bool          `json:"oom"`
	AutoSwap      bool          `json:"auto_swap"`
	CPUPercent    int           `json:"cpu_percent"`
	MemoryPercent int           `json:"memory_percent"`
	DiskPercent   int           `json:"disk_percent"`
	SSLExpiryDays int           `json:"ssl_expiry_days"`
	Cooldown      time.Duration `json:"-"`
	CooldownMins  int           `json:"cooldown_minutes"`
	SMTP          SMTPConfig    `json:"smtp"`
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
		SMTP: SMTPConfig{Port: 587, Transport: SMTPSTARTTLS}}
}

var smtpHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

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
	return ValidateSMTPConfig(settings.SMTP)
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
	return settings
}
