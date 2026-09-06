package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type SiteKind string

type WordPressMultisiteMode string

const (
	WordPress    SiteKind = "wordpress"
	PHP          SiteKind = "php"
	Python       SiteKind = "python"
	Static       SiteKind = "static"
	ReverseProxy SiteKind = "reverse_proxy"

	MultisiteDisabled       WordPressMultisiteMode = ""
	MultisiteSubdirectories WordPressMultisiteMode = "subdirectories"
	MultisiteSubdomains     WordPressMultisiteMode = "subdomains"
)

var siteIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,47}$`)
var siteUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var domainPattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)$`)

func ValidateDomain(domain string) error {
	if !domainPattern.MatchString(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))) {
		return errors.New("domain must be a fully qualified ASCII hostname")
	}
	return nil
}

func ValidSiteKind(kind SiteKind) bool {
	switch kind {
	case WordPress, PHP, Python, Static, ReverseProxy:
		return true
	default:
		return false
	}
}

func ValidateSite(site Site) error {
	if err := ValidateSiteID(site.ID); err != nil {
		return err
	}
	if !ValidSiteKind(site.Kind) {
		return errors.New("invalid site kind")
	}
	if site.Environment != "" && site.Environment != "production" && site.Environment != "staging" {
		return errors.New("invalid site environment")
	}
	if site.Environment == "staging" {
		if site.Kind != WordPress || ValidateSiteID(site.ParentSiteID) != nil || site.ParentSiteID == site.ID {
			return errors.New("staging must reference another WordPress site")
		}
	} else if site.ParentSiteID != "" {
		return errors.New("only staging sites may reference a production site")
	}
	if err := ValidateDomain(site.Domain); err != nil {
		return err
	}
	if site.Kind == WordPress || site.Kind == PHP {
		if !ValidPHPVersion(site.PHPVersion) {
			return errors.New("PHP version must be between 7.1 and 8.5")
		}
		if IsEOLPHP(site.PHPVersion) && !site.AllowEOL {
			return errors.New("this PHP branch is end-of-life; advanced confirmation is required")
		}
	} else if site.PHPVersion != "" {
		return errors.New("PHP version is only valid for WordPress and PHP sites")
	}
	if site.WordPressMultisite != MultisiteDisabled {
		if site.Kind != WordPress || (site.WordPressMultisite != MultisiteSubdirectories && site.WordPressMultisite != MultisiteSubdomains) {
			return errors.New("WordPress multisite mode is invalid")
		}
	}
	if site.Kind == ReverseProxy {
		upstream, err := url.Parse(site.Upstream)
		if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" || upstream.User != nil || upstream.Fragment != "" {
			return errors.New("reverse proxy upstream must be an http or https URL without credentials or fragment")
		}
		if strings.ContainsAny(site.Upstream, "\r\n") {
			return errors.New("reverse proxy upstream contains a line break")
		}
	} else if site.Upstream != "" {
		return errors.New("upstream is only valid for reverse proxy sites")
	}
	return nil
}

func IsEOLPHP(version string) bool {
	switch version {
	case "7.1", "7.2", "7.3", "7.4", "8.0", "8.1":
		return true
	default:
		return false
	}
}

func ValidPHPVersion(version string) bool {
	for minor := 0; minor <= 5; minor++ {
		if version == fmt.Sprintf("8.%d", minor) {
			return true
		}
	}
	for minor := 1; minor <= 4; minor++ {
		if version == fmt.Sprintf("7.%d", minor) {
			return true
		}
	}
	return false
}

func ValidateSiteID(id string) error {
	if siteUUIDPattern.MatchString(id) {
		return nil
	}
	// Installed sites may still use their original slug in database records,
	// filesystem paths, and host configuration. Keep precisely that old grammar;
	// accepting generated UUIDs must not require renaming those resources.
	if !siteIDPattern.MatchString(id) || strings.Contains(id, "--") || strings.HasSuffix(id, "-") {
		return errors.New("site id must be a lowercase UUID v4 or a legacy site identifier")
	}
	return nil
}

type Site struct {
	ID                  string                 `json:"id"`
	Domain              string                 `json:"domain"`
	Kind                SiteKind               `json:"kind"`
	PHPVersion          string                 `json:"php_version,omitempty"`
	Upstream            string                 `json:"upstream,omitempty"`
	AllowEOL            bool                   `json:"allow_eol,omitempty"`
	Status              string                 `json:"status"`
	TLSStatus           string                 `json:"tls_status"`
	CreatedAt           string                 `json:"created_at"`
	Environment         string                 `json:"environment,omitempty"`
	ParentSiteID        string                 `json:"parent_site_id,omitempty"`
	RedisEnabled        bool                   `json:"redis_enabled,omitempty"`
	FastCGICacheEnabled bool                   `json:"fastcgi_cache_enabled,omitempty"`
	WordPressMultisite  WordPressMultisiteMode `json:"wordpress_multisite,omitempty"`
}
