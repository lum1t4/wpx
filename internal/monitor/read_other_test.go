//go:build !linux

package monitor

import (
	"strings"
	"testing"
)

func TestReadSystemReportsUnsupportedPlatform(t *testing.T) {
	if _, err := ReadSystem([]string{"/"}); err == nil || !strings.Contains(err.Error(), "only available on Linux") {
		t.Fatalf("development host must not report fabricated measurements: %v", err)
	}
}
