package provision

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestObservabilityIsLocalAndBounded(t *testing.T) {
	host := testHost(t, &recordRunner{})
	public := filepath.Join(host.SiteRoot, "example-com", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "one.txt"), []byte("12345"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "two.txt"), []byte("1234567"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(host.NginxLogRoot, 0750); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&log, "request %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(host.NginxLogRoot, "wpx-example-com-access.log"), []byte(log.String()), 0640); err != nil {
		t.Fatal(err)
	}
	result, err := host.Observability(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	if result.DiskBytes != 12 || result.FileCount != 2 || len(result.AccessLog) != maxLogLines || result.AccessLog[0] != "request 050" || result.AccessLog[199] != "request 249" {
		t.Fatalf("unexpected observability result: %#v", result)
	}
}

func TestDiskUsageScanStopsAtHardFileLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0640); err != nil {
			t.Fatal(err)
		}
	}
	_, files, truncated, err := scanDiskUsage(context.Background(), root, 2)
	if err != nil || files != 2 || !truncated {
		t.Fatalf("files=%d truncated=%v err=%v", files, truncated, err)
	}
}
