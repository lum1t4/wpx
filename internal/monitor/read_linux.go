//go:build linux

package monitor

import (
	"fmt"
	"math"
	"path/filepath"
	"syscall"
	"time"
)

// ReadSystem reads local kernel counters only. It does not start subprocesses,
// inspect other processes' environments, or contact a remote monitoring service.
func ReadSystem(paths []string) (Raw, error) {
	return readAt("/proc", paths, time.Now(), readDisk)
}

func readAt(procRoot string, paths []string, now time.Time, diskUsage func(string) (Disk, error)) (Raw, error) {
	raw := Raw{At: now}
	read := func(name string) (string, error) {
		data, err := readProc(filepath.Join(procRoot, name))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		return data, nil
	}
	data, err := read("stat")
	if err != nil {
		return Raw{}, err
	}
	raw.CPU, raw.CPUCount, err = parseCPU(data)
	if err != nil {
		return Raw{}, fmt.Errorf("read CPU usage: %w", err)
	}
	data, err = read("meminfo")
	if err != nil {
		return Raw{}, err
	}
	raw.MemoryTotal, raw.MemoryAvailable, raw.SwapTotal, raw.SwapFree, err = parseMemory(data)
	if err != nil {
		return Raw{}, fmt.Errorf("read memory usage: %w", err)
	}
	data, err = read("net/dev")
	if err != nil {
		return Raw{}, err
	}
	raw.Network, err = parseNetwork(data)
	if err != nil {
		return Raw{}, fmt.Errorf("read network usage: %w", err)
	}
	data, err = read("loadavg")
	if err != nil {
		return Raw{}, err
	}
	raw.Load1, raw.Load5, raw.Load15, err = parseLoad(data)
	if err != nil {
		return Raw{}, fmt.Errorf("read load averages: %w", err)
	}
	data, err = read("uptime")
	if err != nil {
		return Raw{}, err
	}
	raw.Uptime, err = parseUptime(data)
	if err != nil {
		return Raw{}, fmt.Errorf("read uptime: %w", err)
	}
	// A missing site root should not hide CPU and memory readings. Keep disk
	// failures attached to their paths so operators can see which mount failed.
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		disk, err := diskUsage(path)
		if err != nil {
			disk = Disk{Error: err.Error()}
		}
		disk.Path = path
		raw.Disks = append(raw.Disks, disk)
	}
	return raw, nil
}

func readDisk(path string) (Disk, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Disk{}, fmt.Errorf("read filesystem: %w", err)
	}
	return diskFromStat(path, stat)
}

func diskFromStat(path string, stat syscall.Statfs_t) (Disk, error) {
	if stat.Bsize <= 0 || stat.Bfree > stat.Blocks || stat.Bavail > stat.Bfree || stat.Blocks > math.MaxUint64/uint64(stat.Bsize) {
		return Disk{}, fmt.Errorf("invalid filesystem capacity")
	}
	blockSize := uint64(stat.Bsize)
	return Disk{
		Path:      path,
		Total:     stat.Blocks * blockSize,
		Used:      (stat.Blocks - stat.Bfree) * blockSize,
		Available: stat.Bavail * blockSize,
	}, nil
}
