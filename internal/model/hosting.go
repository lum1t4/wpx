package model

import (
	"errors"
	"net"
	"path"
	"regexp"
	"strings"
)

type NodeRuntime struct {
	SiteID      string   `json:"site_id"`
	Entrypoint  string   `json:"entrypoint"`
	Arguments   []string `json:"arguments,omitempty"`
	Port        int      `json:"port"`
	NodeVersion string   `json:"node_version"`
	Status      string   `json:"status"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

type FTPUser struct {
	ID           string `json:"id"`
	SiteID       string `json:"site_id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash,omitempty"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type MailService struct {
	Enabled   bool   `json:"enabled"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

var ftpUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
var nodeVersionPattern = regexp.MustCompile(`^24\.20\.0$`)

func ValidateNodeRuntime(site Site, runtime NodeRuntime) error {
	if site.Kind != ReverseProxy || runtime.SiteID != site.ID {
		return errors.New("Node runtime requires its matching reverse proxy site")
	}
	if runtime.Port < 1024 || runtime.Port > 65535 {
		return errors.New("Node port must be between 1024 and 65535")
	}
	upstream := strings.TrimSuffix(site.Upstream, "/")
	if upstream != "http://127.0.0.1:"+itoa(runtime.Port) && upstream != "http://localhost:"+itoa(runtime.Port) {
		return errors.New("reverse proxy upstream must use the Node runtime loopback port")
	}
	clean := path.Clean(runtime.Entrypoint)
	if clean == "." || clean != runtime.Entrypoint || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || strings.ContainsAny(clean, "\x00\r\n") {
		return errors.New("Node entrypoint must be a clean relative path")
	}
	if !nodeVersionPattern.MatchString(runtime.NodeVersion) {
		return errors.New("Node version must be 24.20.0")
	}
	if len(runtime.Arguments) > 16 {
		return errors.New("Node runtime accepts at most 16 arguments")
	}
	for _, argument := range runtime.Arguments {
		if argument == "" || len(argument) > 256 || strings.ContainsAny(argument, "\x00\r\n") {
			return errors.New("Node argument is invalid")
		}
	}
	return nil
}

func ValidateFTPUser(user FTPUser) error {
	if ValidateSiteID(user.SiteID) != nil || !ftpUsernamePattern.MatchString(user.Username) {
		return errors.New("FTP username must be 3-32 lowercase letters, digits, underscores, or hyphens")
	}
	if user.PasswordHash == "" || len(user.PasswordHash) > 128 || (!strings.HasPrefix(user.PasswordHash, "$2a$") && !strings.HasPrefix(user.PasswordHash, "$2b$") && !strings.HasPrefix(user.PasswordHash, "$2y$")) {
		return errors.New("FTP password hash is invalid")
	}
	return nil
}

func ValidateMailService(service MailService) error {
	if !service.Enabled {
		return nil
	}
	if ValidateDomain(service.Hostname) != nil || net.ParseIP(service.Hostname) != nil {
		return errors.New("mail hostname must be a fully qualified hostname")
	}
	return nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for value > 0 {
		i--
		b[i] = byte('0' + value%10)
		value /= 10
	}
	return string(b[i:])
}
