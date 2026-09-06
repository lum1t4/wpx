package model

import "errors"

// PHPVersionChange records both sides of the operation. The stored site keeps
// its current version until the worker confirms the new runtime is active.
type PHPVersionChange struct {
	PreviousVersion string `json:"previous_version"`
	Version         string `json:"version"`
	AllowEOL        bool   `json:"allow_eol"`
}

// PHPVersionChangeError distinguishes a rejected change whose previous runtime
// is confirmed ready from a failure that still needs recovery. The job fails in
// both cases, but only confirmed recovery can unlock the site's ordinary tools.
type PHPVersionChangeError struct {
	Err              error
	PreviousRestored bool
}

func (e *PHPVersionChangeError) Error() string { return e.Err.Error() }
func (e *PHPVersionChangeError) Unwrap() error { return e.Err }

func ValidatePHPVersionChange(site Site, change PHPVersionChange) error {
	if err := ValidateSite(site); err != nil {
		return err
	}
	if site.Kind != WordPress && site.Kind != PHP {
		return errors.New("PHP version changes require a WordPress or PHP site")
	}
	if change.PreviousVersion != site.PHPVersion || change.Version == change.PreviousVersion {
		return errors.New("PHP version change does not match the current site version")
	}
	target := site
	target.PHPVersion, target.AllowEOL = change.Version, change.AllowEOL
	return ValidateSite(target)
}
