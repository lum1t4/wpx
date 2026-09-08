package model

import "errors"

const WordPressDebugLogLimit = 256 << 10

// WordPressDebugStatus describes host state without exposing the private log
// pathname to the web process or browser.
type WordPressDebugStatus struct {
	Enabled       bool   `json:"enabled"`
	Known         bool   `json:"known"`
	Managed       bool   `json:"managed"`
	LogExists     bool   `json:"log_exists"`
	LogSize       int64  `json:"log_size"`
	LogModifiedAt string `json:"log_modified_at,omitempty"`
}

type WordPressDebugLog struct {
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

func ValidateWordPressDebugSite(site Site) error {
	if err := ValidateSite(site); err != nil {
		return err
	}
	if site.Kind != WordPress || (site.Status != "active" && site.Status != "disabled") {
		return errors.New("WordPress debug requires an active or disabled WordPress site")
	}
	return nil
}
