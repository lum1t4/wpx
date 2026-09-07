package provision

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

var operatorServices = []string{"wpx.service", "wpx-broker.service", "nginx.service", "mariadb.service", "redis-server.service"}
var diagnosticSecret = regexp.MustCompile(`(?i)(password|passwd|token|secret|authorization)([[:space:]]*[:=][[:space:]]*)[^[:space:]]+`)

func (h *Host) OperatorDiagnostics(ctx context.Context, sites []model.Site, runtimes []model.NodeRuntime) (broker.OperatorDiagnosticsResult, error) {
	if h.Output == nil {
		return broker.OperatorDiagnosticsResult{}, errors.New("command output is unavailable")
	}
	serviceNames := append([]string(nil), operatorServices...)
	seenService := make(map[string]bool, len(serviceNames))
	for _, name := range serviceNames {
		seenService[name] = true
	}
	for _, site := range sites {
		if site.Status != "active" {
			continue
		}
		var names []string
		switch site.Kind {
		case model.WordPress, model.PHP:
			names = []string{"php" + site.PHPVersion + "-fpm.service"}
		case model.Python:
			names = []string{"wpx-python-" + site.ID + ".socket", "wpx-python-" + site.ID + ".service"}
		}
		for _, name := range names {
			if !seenService[name] {
				seenService[name] = true
				serviceNames = append(serviceNames, name)
			}
		}
	}
	for _, runtime := range runtimes {
		name := "wpx-node-" + runtime.SiteID + ".service"
		if runtime.Status == "active" && !seenService[name] {
			seenService[name] = true
			serviceNames = append(serviceNames, name)
		}
	}
	result := broker.OperatorDiagnosticsResult{Services: make([]broker.OperatorServiceStatus, 0, len(serviceNames))}
	for _, name := range serviceNames {
		required := !(strings.HasPrefix(name, "wpx-python-") && strings.HasSuffix(name, ".service"))
		service := broker.OperatorServiceStatus{Name: name, Active: "unknown", Sub: "unknown", Required: required}
		output, err := h.Output.Output(ctx, "/usr/bin/systemctl", "show", name, "--property=ActiveState,SubState,NRestarts,ExecMainStatus", "--no-pager")
		if err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				switch key {
				case "ActiveState":
					service.Active = boundedText(value, 64)
				case "SubState":
					service.Sub = boundedText(value, 64)
				case "NRestarts":
					service.Restarts, _ = strconv.Atoi(value)
				case "ExecMainStatus":
					service.ExitStatus, _ = strconv.Atoi(value)
				}
			}
		}
		if err != nil || service.Active != "active" {
			if journal, journalErr := h.Output.Output(ctx, "/usr/bin/journalctl", "-u", name, "-n", "20", "--no-pager", "--output=short-iso"); journalErr == nil {
				service.Diagnostics = boundedLines(string(journal), 20, 4096)
			}
		}
		result.Services = append(result.Services, service)
	}
	if output, err := h.Output.Output(ctx, "/usr/bin/journalctl", "-k", "--since=-10min", "-n", "40", "--no-pager", "--output=short-iso"); err == nil {
		for _, line := range boundedLines(string(output), 40, 8192) {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "out of memory") || strings.Contains(lower, "oom-kill") || strings.Contains(lower, "killed process") {
				result.OOMEvents = append(result.OOMEvents, line)
			}
			if len(result.OOMEvents) == 10 {
				break
			}
		}
	}
	for _, site := range sites {
		if site.TLSStatus != "active" {
			continue
		}
		status := broker.OperatorCertificateStatus{SiteID: site.ID, Domain: site.Domain}
		path := filepath.Join(h.CertificateRoot, site.Domain, "fullchain.pem")
		resolved, err := confinedCertificatePath(h.CertificateRoot, path)
		info, statErr := os.Stat(resolved)
		if err != nil || statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			status.Error = "certificate file is unavailable"
		} else {
			data, readErr := os.ReadFile(resolved)
			block, _ := pem.Decode(data)
			if readErr != nil || block == nil || block.Type != "CERTIFICATE" {
				status.Error = "certificate file is invalid"
			} else if cert, parseErr := x509.ParseCertificate(block.Bytes); parseErr != nil {
				status.Error = "certificate file is invalid"
			} else {
				status.NotAfter = cert.NotAfter.UTC().Format("2006-01-02T15:04:05Z07:00")
			}
		}
		result.Certificates = append(result.Certificates, status)
	}
	return result, nil
}

func (h *Host) EnsureOperatorSwap(ctx context.Context, sizeMiB int) (string, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sizeMiB < 512 || sizeMiB > 8192 || sizeMiB%512 != 0 {
		return "", false, errors.New("invalid managed swap size")
	}
	if !filepath.IsAbs(h.DataRoot) || filepath.Clean(h.DataRoot) == "/" {
		return "", false, errors.New("data root must be an absolute non-root path")
	}
	if h.Runner == nil || h.Output == nil {
		return "", false, errors.New("managed swap dependencies are unavailable")
	}
	if info, err := os.Lstat(h.DataRoot); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("data root must not be a symlink")
	}
	directory := filepath.Join(h.DataRoot, "swap")
	path, marker := filepath.Join(directory, "wpx.swap"), filepath.Join(directory, ".wpx-managed")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", false, fmt.Errorf("create swap directory: %w", err)
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("managed swap directory is not trusted")
	}
	realRoot, err := filepath.EvalSymlinks(h.DataRoot)
	if err != nil {
		return "", false, fmt.Errorf("resolve data root: %w", err)
	}
	realDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || realDirectory != filepath.Join(realRoot, "swap") {
		return "", false, errors.New("managed swap directory escapes the data root")
	}
	if info, err := os.Lstat(path); err == nil {
		markerInfo, markerStatErr := os.Lstat(marker)
		markerData, markerErr := os.ReadFile(marker)
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || markerStatErr != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerErr != nil || string(markerData) != ownershipMarker {
			return "", false, errors.New("refuse unmanaged swap file")
		}
		if uint64(info.Size()) != uint64(sizeMiB)*1024*1024 {
			return "", false, errors.New("managed swap has a different size")
		}
		active, _ := h.Output.Output(ctx, "/sbin/swapon", "--show=NAME", "--noheadings")
		for _, line := range strings.Fields(string(active)) {
			if filepath.Clean(line) == path {
				return path, false, nil
			}
		}
		if err := h.Runner.Run(ctx, "/sbin/swapon", path); err != nil {
			return "", false, err
		}
		return path, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(directory, &disk); err != nil {
		return "", false, fmt.Errorf("inspect swap filesystem capacity: %w", err)
	}
	required := uint64(sizeMiB)*1024*1024 + 1024*1024*1024
	if uint64(disk.Bavail)*uint64(disk.Bsize) < required {
		return "", false, errors.New("insufficient disk space for managed swap and 1 GiB reserve")
	}
	if err := atomicWrite(marker, []byte(ownershipMarker), 0600); err != nil {
		return "", false, err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/fallocate", "-l", fmt.Sprintf("%dM", sizeMiB), path); err != nil {
		return "", false, fmt.Errorf("allocate swap: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if err := os.Chmod(path, 0600); err != nil {
		return "", false, err
	}
	if err := h.Runner.Run(ctx, "/sbin/mkswap", path); err != nil {
		return "", false, fmt.Errorf("format swap: %w", err)
	}
	if err := h.Runner.Run(ctx, "/sbin/swapon", path); err != nil {
		return "", false, fmt.Errorf("activate swap: %w", err)
	}
	cleanup = false
	return path, true, nil
}

func confinedCertificatePath(liveRoot, candidate string) (string, error) {
	if !filepath.IsAbs(liveRoot) || filepath.Clean(liveRoot) == "/" {
		return "", errors.New("certificate root is invalid")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	// Certbot's live/domain files normally point into the sibling archive tree.
	// Both trees remain beneath the same administrator-owned Let's Encrypt root.
	trustRoot := filepath.Dir(filepath.Clean(liveRoot))
	relative, err := filepath.Rel(trustRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("certificate link escapes its trusted root")
	}
	return resolved, nil
}

func boundedLines(value string, maximum, bytes int) []string {
	if len(value) > bytes {
		value = value[len(value)-bytes:]
	}
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > maximum {
		lines = lines[len(lines)-maximum:]
	}
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, boundedText(diagnosticSecret.ReplaceAllString(line, "$1$2[redacted]"), 512))
		}
	}
	return result
}

func boundedText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
