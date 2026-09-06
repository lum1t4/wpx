//go:build linux

package monitor

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func procFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"stat": cpuFixture, "meminfo": memoryFixture, "net/dev": networkFixture,
		"loadavg": "0.12 1.34 2.56 1/500 1234\n", "uptime": "1234.25 12000.50\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadAtCollectsOneSampleAndKeepsDiskFailuresLocal(t *testing.T) {
	now := time.Unix(1000, 0)
	var calls []string
	diskUsage := func(path string) (Disk, error) {
		calls = append(calls, path)
		if path == "/missing" {
			return Disk{}, errors.New("no such file or directory")
		}
		return Disk{Total: 1000, Used: 500, Available: 400}, nil
	}
	raw, err := readAt(procFixture(t), []string{"/", "/missing", "/", "/sites"}, now, diskUsage)
	if err != nil {
		t.Fatal(err)
	}
	if !raw.At.Equal(now) || raw.CPU.Total != 621 || raw.CPUCount != 2 || raw.MemoryTotal != 1000*1024 || raw.Load5 != 1.34 || raw.Uptime != 1234*time.Second+250*time.Millisecond {
		t.Fatalf("unexpected sample: %+v", raw)
	}
	if len(calls) != 3 || strings.Join(calls, ",") != "/,/missing,/sites" || len(raw.Disks) != 3 {
		t.Fatalf("disk paths were not deduplicated in order: %+v, %+v", calls, raw.Disks)
	}
	if raw.Disks[0].Path != "/" || raw.Disks[0].Available != 400 || raw.Disks[1].Error == "" || raw.Disks[1].Path != "/missing" || raw.Disks[1].Total != 0 {
		t.Fatalf("disk errors are not isolated: %+v", raw.Disks)
	}
}

func TestReadAtFailsClosedForMissingRequiredProcFiles(t *testing.T) {
	for _, name := range []string{"stat", "meminfo", "net/dev", "loadavg", "uptime"} {
		t.Run(name, func(t *testing.T) {
			dir := procFixture(t)
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			_, err := readAt(dir, nil, time.Now(), nil)
			if err == nil || !strings.Contains(err.Error(), name) || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing %s must retain error context, got %v", name, err)
			}
		})
	}
}

func TestReadAtRejectsMalformedRequiredProcFiles(t *testing.T) {
	for _, name := range []string{"stat", "meminfo", "net/dev", "loadavg", "uptime"} {
		t.Run(name, func(t *testing.T) {
			dir := procFixture(t)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readAt(dir, nil, time.Now(), nil); err == nil {
				t.Fatal("malformed kernel data must not become a healthy zero sample")
			}
		})
	}
}

func TestReadSystemOnLinux(t *testing.T) {
	dir := t.TempDir()
	raw, err := ReadSystem([]string{dir, filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if raw.CPUCount < 1 || raw.MemoryTotal == 0 || raw.MemoryAvailable > raw.MemoryTotal || raw.Uptime <= 0 || raw.At.IsZero() {
		t.Fatalf("invalid live Linux sample: %+v", raw)
	}
	if len(raw.Disks) != 2 || raw.Disks[0].Error != "" || raw.Disks[0].Total == 0 || raw.Disks[0].Used > raw.Disks[0].Total || raw.Disks[0].Available > raw.Disks[0].Total-raw.Disks[0].Used || raw.Disks[1].Error == "" {
		t.Fatalf("invalid live disk readings: %+v", raw.Disks)
	}
}

func TestDiskCapacitySeparatesReservedAndUsedBlocks(t *testing.T) {
	disk, err := diskFromStat("/sites", syscall.Statfs_t{Bsize: 4096, Blocks: 100, Bfree: 40, Bavail: 30})
	if err != nil {
		t.Fatal(err)
	}
	if disk.Path != "/sites" || disk.Total != 409600 || disk.Used != 245760 || disk.Available != 122880 {
		t.Fatalf("reserved blocks should not count as used or available: %+v", disk)
	}
}

func TestDiskCapacityRejectsImpossibleOrOverflowingValues(t *testing.T) {
	for _, stat := range []syscall.Statfs_t{
		{Bsize: 0}, {Bsize: -1}, {Bsize: 1, Blocks: 10, Bfree: 11},
		{Bsize: 1, Blocks: 10, Bfree: 5, Bavail: 6}, {Bsize: 4096, Blocks: math.MaxUint64},
	} {
		if _, err := diskFromStat("/sites", stat); err == nil {
			t.Errorf("accepted invalid filesystem capacity: %+v", stat)
		}
	}
}
