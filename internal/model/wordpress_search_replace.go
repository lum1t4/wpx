package model

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxWordPressSearchReplaceBytes = 1024

// WordPressSearchReplace contains literal database values. It deliberately has
// no table, column, regular-expression, or command-option fields.
type WordPressSearchReplace struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

func ValidateWordPressSearchReplace(change WordPressSearchReplace) error {
	if change.Search == "" || strings.TrimSpace(change.Search) == "" {
		return errors.New("search value is required")
	}
	if len(change.Search) > MaxWordPressSearchReplaceBytes || len(change.Replace) > MaxWordPressSearchReplaceBytes {
		return errors.New("search and replacement values must not exceed 1024 bytes")
	}
	if !utf8.ValidString(change.Search) || !utf8.ValidString(change.Replace) {
		return errors.New("search and replacement values must be valid UTF-8")
	}
	if change.Search == change.Replace {
		return errors.New("search and replacement values must differ")
	}
	// WP-CLI recognizes associative options anywhere in argv. Reject an operand
	// that it could reinterpret instead of accepting user-selected CLI flags.
	if strings.HasPrefix(change.Search, "-") || strings.HasPrefix(change.Replace, "-") {
		return errors.New("search and replacement values must not begin with a hyphen")
	}
	for _, value := range []string{change.Search, change.Replace} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return errors.New("search and replacement values must not contain control characters")
			}
		}
	}
	return nil
}
