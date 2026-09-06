package model

import (
	"crypto/rand"
	"fmt"
)

// NewSiteID gives a site an identity independent of its domain or display name.
// The RFC 4122 version and variant bits leave 122 random bits; callers must still
// treat the database uniqueness constraint as the final authority on collisions.
func NewSiteID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate site identifier: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}
