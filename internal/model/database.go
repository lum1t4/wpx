package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

type Database struct {
	ID         string `json:"id"`
	SiteID     string `json:"site_id,omitempty"`
	SiteDomain string `json:"site_domain,omitempty"`
	Label      string `json:"label"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

var sqlIdentifierPattern = regexp.MustCompile(`^wpx_[0-9a-f]{16}$`)

func NewDatabaseID() (string, error) { return NewSiteID() }

func DatabaseIdentifiers(id string) (string, string, error) {
	if !siteUUIDPattern.MatchString(id) {
		return "", "", errors.New("database id must be a UUID v4")
	}
	digest := sha256.Sum256([]byte(id))
	identifier := "wpx_" + hex.EncodeToString(digest[:8])
	return identifier, identifier, nil
}

func ValidateDatabase(database Database) error {
	wantName, wantUser, err := DatabaseIdentifiers(database.ID)
	if err != nil {
		return err
	}
	if database.SiteID != "" {
		if err := ValidateSiteID(database.SiteID); err != nil {
			return err
		}
	}
	label := strings.TrimSpace(database.Label)
	if len(label) < 2 || len(label) > 64 || strings.ContainsAny(label, "\r\n") {
		return errors.New("database label must be 2-64 characters")
	}
	if database.Name != wantName || database.Username != wantUser || !sqlIdentifierPattern.MatchString(database.Name) {
		return errors.New("database identifiers do not match their UUID")
	}
	if database.Password != "" && (len(database.Password) < 24 || len(database.Password) > 128 || strings.ContainsAny(database.Password, "'\"\\\r\n")) {
		return errors.New("database password is invalid")
	}
	return nil
}
