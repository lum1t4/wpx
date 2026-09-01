package install

import (
	"strings"
	"testing"
)

func TestLocalLogPoliciesHaveTimeAndDiskCaps(t *testing.T) {
	rotation := nginxLogrotatePolicy()
	journal := wpxJournalPolicy()
	for _, expected := range []string{"maxsize 100M", "rotate 14"} {
		if !strings.Contains(rotation, expected) {
			t.Fatalf("Nginx log policy missing %q", expected)
		}
	}
	for _, expected := range []string{"SystemMaxUse=256M", "RuntimeMaxUse=64M", "MaxRetentionSec=14day"} {
		if !strings.Contains(journal, expected) {
			t.Fatalf("journal policy missing %q", expected)
		}
	}
}
