package model

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var siteAccessUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SiteAccessSettings is desired state. PasswordHash crosses the root boundary
// because Nginx needs it, but plaintext credentials never enter SQLite, a job
// payload, an audit event, or a generated Nginx configuration.
type SiteAccessSettings struct {
	SiteID           string `json:"site_id"`
	BasicAuthEnabled bool   `json:"basic_auth_enabled"`
	Username         string `json:"username,omitempty"`
	PasswordHash     string `json:"password_hash,omitempty"`
	CloudflareOnly   bool   `json:"cloudflare_only"`
	Status           string `json:"status,omitempty"`
	LastError        string `json:"last_error,omitempty"`
}

func ValidateSiteAccessUsername(username string) error {
	if !siteAccessUsernamePattern.MatchString(strings.TrimSpace(username)) {
		return errors.New("username must be 1–64 ASCII letters, numbers, dots, dashes, or underscores")
	}
	return nil
}

func ValidateSiteAccessPassword(password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must be between 12 and 72 bytes")
	}
	if strings.TrimSpace(password) != password {
		return errors.New("password cannot begin or end with whitespace")
	}
	return nil
}

func ValidateSiteAccessSettings(settings SiteAccessSettings) error {
	if err := ValidateSiteID(settings.SiteID); err != nil {
		return err
	}
	if settings.Status != "" && settings.Status != "pending" && settings.Status != "active" && settings.Status != "failed" {
		return errors.New("invalid site access status")
	}
	if settings.Username != "" {
		if err := ValidateSiteAccessUsername(settings.Username); err != nil {
			return err
		}
	}
	if settings.PasswordHash != "" {
		cost, err := bcrypt.Cost([]byte(settings.PasswordHash))
		if len(settings.PasswordHash) != 60 || err != nil || cost < 12 {
			return errors.New("basic authentication password hash is invalid")
		}
	}
	if !settings.BasicAuthEnabled {
		return nil
	}
	if settings.Username == "" || settings.PasswordHash == "" {
		return errors.New("basic authentication requires a username and password")
	}
	return nil
}
