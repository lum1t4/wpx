package model

import (
	"errors"
	"regexp"
)

type WordPressComponent string

const (
	WordPressCore   WordPressComponent = "core"
	WordPressPlugin WordPressComponent = "plugin"
	WordPressTheme  WordPressComponent = "theme"
)

var wordpressSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

type WordPressUpdate struct {
	Component WordPressComponent `json:"component"`
	Name      string             `json:"name,omitempty"`
}

func ValidateWordPressUpdate(update WordPressUpdate) error {
	switch update.Component {
	case WordPressCore:
		if update.Name != "" {
			return errors.New("core update must not include a component name")
		}
	case WordPressPlugin, WordPressTheme:
		if !wordpressSlugPattern.MatchString(update.Name) {
			return errors.New("invalid WordPress component slug")
		}
	default:
		return errors.New("unsupported WordPress update component")
	}
	return nil
}
