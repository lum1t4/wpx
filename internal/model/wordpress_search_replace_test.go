package model

import (
	"strings"
	"testing"
)

func TestValidateWordPressSearchReplace(t *testing.T) {
	valid := []WordPressSearchReplace{
		{Search: "https://old.example", Replace: "https://new.example"},
		{Search: "old", Replace: ""},
		{Search: "serialized value", Replace: "replacement value"},
	}
	for _, change := range valid {
		if err := ValidateWordPressSearchReplace(change); err != nil {
			t.Errorf("valid change rejected: %v", err)
		}
	}
	invalid := []WordPressSearchReplace{
		{}, {Search: "   ", Replace: "new"}, {Search: "same", Replace: "same"},
		{Search: "--regex", Replace: "new"}, {Search: "old", Replace: "--all-tables"},
		{Search: "old\x00value", Replace: "new"},
		{Search: strings.Repeat("a", MaxWordPressSearchReplaceBytes+1), Replace: "new"},
		{Search: "old", Replace: strings.Repeat("b", MaxWordPressSearchReplaceBytes+1)},
	}
	for _, change := range invalid {
		if err := ValidateWordPressSearchReplace(change); err == nil {
			t.Errorf("invalid change accepted: %#v", change)
		}
	}
}
