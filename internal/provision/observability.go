package provision

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

const (
	maxLogReadBytes  = 256 << 10
	maxLogLines      = 200
	maxObservedFiles = 100000
)

// Observability reads only host-local state. It neither samples users nor
// sends metrics to WPX or another service.
func (h *Host) Observability(ctx context.Context, site model.Site) (broker.SiteObservabilityResult, error) {
	if err := model.ValidateSite(site); err != nil {
		return broker.SiteObservabilityResult{}, err
	}
	publicDir := filepath.Join(h.SiteRoot, site.ID, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return broker.SiteObservabilityResult{}, err
	}
	if !filepath.IsAbs(h.NginxLogRoot) || h.NginxLogRoot == "/" {
		return broker.SiteObservabilityResult{}, errors.New("invalid nginx log root")
	}
	var result broker.SiteObservabilityResult
	diskBytes, fileCount, truncated, err := scanDiskUsage(ctx, publicDir, maxObservedFiles)
	if err != nil {
		return broker.SiteObservabilityResult{}, err
	}
	result.DiskBytes, result.FileCount, result.ScanTruncated = diskBytes, fileCount, truncated
	result.AccessLog, err = tailLines(filepath.Join(h.NginxLogRoot, "wpx-"+site.ID+"-access.log"))
	if err != nil {
		return broker.SiteObservabilityResult{}, err
	}
	result.ErrorLog, err = tailLines(filepath.Join(h.NginxLogRoot, "wpx-"+site.ID+"-error.log"))
	if err != nil {
		return broker.SiteObservabilityResult{}, err
	}
	return result, nil
}

func scanDiskUsage(ctx context.Context, root string, limit int64) (int64, int64, bool, error) {
	var bytes, files int64
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
			files++
			if files >= limit {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return bytes, files, truncated, err
}

func tailLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("site log is not a regular file")
	}
	start := info.Size() - maxLogReadBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(file, maxLogReadBytes))
	if start > 0 {
		_, _ = reader.ReadString('\n')
	}
	lines := make([]string, 0, maxLogLines)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if len(lines) == maxLogLines {
			copy(lines, lines[1:])
			lines[len(lines)-1] = line
		} else {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}
