package model

import (
	"errors"
	"strings"
)

// DomainChange keeps both sides of a queued rename. The site's identity and
// old domain remain authoritative until the host confirms the change. TLS is
// deliberately not transferred: the old certificate does not cover a new name.
type DomainChange struct {
	PreviousDomain    string `json:"previous_domain"`
	Domain            string `json:"domain"`
	PreviousTLSStatus string `json:"previous_tls_status"`
}

// DomainChangeError unlocks ordinary site tools only when host recovery has
// actually restored the previous database and Nginx configuration. An unknown
// result must retain the reservation so the same job can resume its journal.
type DomainChangeError struct {
	Err              error
	PreviousRestored bool
}

func (e *DomainChangeError) Error() string { return e.Err.Error() }
func (e *DomainChangeError) Unwrap() error { return e.Err }

func ValidateDomainChange(site Site, change DomainChange) error {
	if err := ValidateSite(site); err != nil {
		return err
	}
	if change.PreviousDomain != site.Domain || change.PreviousTLSStatus != site.TLSStatus {
		return errors.New("domain change does not match the current site domain and TLS state")
	}
	if site.Domain != strings.ToLower(strings.TrimSuffix(strings.TrimSpace(site.Domain), ".")) || len(site.Domain) > 253 {
		return errors.New("current site domain is not normalized")
	}
	if change.Domain != strings.ToLower(strings.TrimSuffix(strings.TrimSpace(change.Domain), ".")) || ValidateDomain(change.Domain) != nil || len(change.Domain) > 253 {
		return errors.New("new domain must be a normalized fully qualified ASCII hostname")
	}
	if strings.EqualFold(change.Domain, strings.TrimSuffix(site.Domain, ".")) {
		return errors.New("new domain must differ from the current domain")
	}
	if site.Kind == WordPress && site.WordPressMultisite != MultisiteDisabled {
		return errors.New("domain changes for WordPress multisite are not supported; network URLs require a separate migration")
	}
	return nil
}
