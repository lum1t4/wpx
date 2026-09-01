package install

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestInstallerRejectsUnsafeSystemdConfigurationPathBeforeHostChanges(t *testing.T) {
	_, err := Run(context.Background(), Options{DryRun: true, ConfigPath: "/etc/wpx/config.json\nExecStart=/tmp/untrusted", Output: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "contain no whitespace") {
		t.Fatalf("error=%v", err)
	}
}
