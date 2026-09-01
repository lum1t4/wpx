package model

import (
	"errors"
	pathpkg "path"
	"regexp"
	"strings"
)

var databaseTablePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

// StagingSelection is persisted with the durable deployment job. Full is
// mutually exclusive with Files and Tables.
type StagingSelection struct {
	Full   bool     `json:"full"`
	Files  []string `json:"files,omitempty"`
	Tables []string `json:"tables,omitempty"`
}

func ValidateStagingSelection(selection StagingSelection) error {
	if selection.Full {
		if len(selection.Files) != 0 || len(selection.Tables) != 0 {
			return errors.New("full staging deployment cannot include custom selections")
		}
		return nil
	}
	if len(selection.Files) == 0 && len(selection.Tables) == 0 {
		return errors.New("select at least one file or database table")
	}
	if len(selection.Files) > 5000 || len(selection.Tables) > 1000 {
		return errors.New("custom staging deployment exceeds selection limits")
	}
	seen := make(map[string]struct{}, len(selection.Files)+len(selection.Tables))
	for _, name := range selection.Files {
		if name == "" || name != pathpkg.Clean(name) || pathpkg.IsAbs(name) || name == "." || strings.HasPrefix(name, "../") || name == "wp-config.php" || strings.HasPrefix(name, "wpx-login-") || name == "wp-content/mu-plugins/wpx-staging.php" {
			return errors.New("custom staging file selection is invalid")
		}
		key := "file:" + name
		if _, exists := seen[key]; exists {
			return errors.New("custom staging selection contains duplicates")
		}
		seen[key] = struct{}{}
	}
	for _, table := range selection.Tables {
		if !databaseTablePattern.MatchString(table) {
			return errors.New("custom staging table selection is invalid")
		}
		key := "table:" + table
		if _, exists := seen[key]; exists {
			return errors.New("custom staging selection contains duplicates")
		}
		seen[key] = struct{}{}
	}
	return nil
}
