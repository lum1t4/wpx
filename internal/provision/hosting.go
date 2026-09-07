package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) ApplyNodeRuntime(ctx context.Context, site model.Site, runtime model.NodeRuntime) (returnErr error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := model.ValidateNodeRuntime(site, runtime); err != nil {
		return err
	}
	if h.Runner == nil || !safeHostingRoot(h.SiteRoot) || !safeHostingRoot(h.HostingUnitRoot) || !safeHostingRoot(h.NodeInstallRoot) {
		return errors.New("Node runtime dependencies are unavailable")
	}
	nodeBinary, err := h.ensureNodeRuntime(ctx)
	if err != nil {
		return err
	}
	public := filepath.Join(h.SiteRoot, site.ID, "public")
	entrypoint, err := confinedExistingFile(public, runtime.Entrypoint)
	if err != nil {
		return err
	}
	if h.Identities == nil {
		return errors.New("site identity manager is unavailable")
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return fmt.Errorf("lookup site account: %w", err)
	}
	unitName := "wpx-node-" + site.ID + ".service"
	unitPath := filepath.Join(h.HostingUnitRoot, unitName)
	var previousUnit []byte
	if _, err := os.Lstat(unitPath); err == nil {
		var exists bool
		previousUnit, exists, err = managedFileState(unitPath)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("Node runtime unit disappeared during inspection")
		}
		if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "stop", unitName); err != nil {
			return fmt.Errorf("stop existing Node runtime: %w", err)
		}
		defer func() {
			if returnErr != nil {
				_ = atomicWrite(unitPath, previousUnit, 0644)
				_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "daemon-reload")
				_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "start", unitName)
			}
		}()
	} else if !os.IsNotExist(err) {
		return err
	}
	if listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(runtime.Port))); err != nil {
		return fmt.Errorf("Node port %d is already in use", runtime.Port)
	} else {
		listener.Close()
	}
	args := []string{nodeBinary, entrypoint}
	args = append(args, runtime.Arguments...)
	unit := ownershipMarker + "[Unit]\nDescription=WPX Node application for " + site.ID + "\nAfter=network.target\n\n[Service]\nType=simple\nUser=" + identity.Name + "\nGroup=" + identity.Name + "\nWorkingDirectory=" + public + "\nEnvironment=HOST=127.0.0.1\nEnvironment=PORT=" + strconv.Itoa(runtime.Port) + "\nExecStart=" + joinSystemdArguments(args) + "\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\nReadWritePaths=" + filepath.Join(h.SiteRoot, site.ID) + "\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\nIPAddressDeny=any\nIPAddressAllow=localhost\nRestart=on-failure\nRestartSec=2s\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := writeManagedUnit(unitPath, unit); err != nil {
		return err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd units: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", unitName); err != nil {
		return fmt.Errorf("start Node runtime: %w", err)
	}
	return nil
}

const nodeRuntimeVersion = "24.20.0"

var nodeRuntimeSHA256 = map[string]string{"amd64": "2f2c0da162318f0de47665410c7c8c2ed3d36c8f3105de4bbc61176c70a7cbf2", "arm64": "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7"}

func (h *Host) ensureNodeRuntime(ctx context.Context) (string, error) {
	archiveArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	expected := nodeRuntimeSHA256[runtime.GOARCH]
	if archiveArch == "" || expected == "" {
		return "", fmt.Errorf("Node runtime is unavailable for %s", runtime.GOARCH)
	}
	if !safeHostingRoot(h.NodeInstallRoot) {
		return "", errors.New("invalid Node installation root")
	}
	root := filepath.Join(h.NodeInstallRoot, "node-v"+nodeRuntimeVersion)
	binary := filepath.Join(root, "bin", "node")
	marker := filepath.Join(root, ".wpx-node-runtime")
	if st, err := os.Stat(binary); err == nil && st.Mode().IsRegular() {
		if raw, markerErr := os.ReadFile(marker); markerErr == nil && string(raw) == nodeRuntimeVersion+"\n" {
			return binary, nil
		}
		return "", errors.New("refuse unmarked Node installation directory")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(h.NodeInstallRoot, 0755); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(h.NodeInstallRoot, ".node-install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	archive := filepath.Join(temporary, "node.tar.xz")
	url := "https://nodejs.org/dist/v" + nodeRuntimeVersion + "/node-v" + nodeRuntimeVersion + "-linux-" + archiveArch + ".tar.xz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Hostname() != "nodejs.org" {
			return errors.New("Node.js download redirected outside nodejs.org HTTPS")
		}
		if len(via) > 5 {
			return errors.New("too many Node.js download redirects")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download Node.js: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Node.js: unexpected HTTP status %s", response.Status)
	}
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, 101<<20))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > 100<<20 {
		return "", errors.New("downloaded Node.js archive exceeds 100 MiB")
	}
	digestBytes := hasher.Sum(nil)
	if hex.EncodeToString(digestBytes) != expected {
		return "", errors.New("downloaded Node.js archive checksum does not match")
	}
	stage := filepath.Join(temporary, "runtime")
	if err := os.Mkdir(stage, 0755); err != nil {
		return "", err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/tar", "--extract", "--xz", "--file", archive, "--directory", stage, "--strip-components=1", "--no-same-owner"); err != nil {
		return "", fmt.Errorf("extract Node.js: %w", err)
	}
	stageBinary := filepath.Join(stage, "bin", "node")
	if st, err := os.Stat(stageBinary); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("installed Node.js binary is missing")
	}
	if err := os.WriteFile(filepath.Join(stage, ".wpx-node-runtime"), []byte(nodeRuntimeVersion+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.Rename(stage, root); err != nil {
		return "", fmt.Errorf("activate Node.js runtime: %w", err)
	}
	return binary, nil
}

func confinedExistingFile(root, relative string) (string, error) {
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("inspect site public directory: %w", err)
	}
	candidate := filepath.Join(rootEval, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("inspect Node entrypoint: %w", err)
	}
	rel, err := filepath.Rel(rootEval, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("Node entrypoint escapes the site public directory")
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("Node entrypoint must be a regular file")
	}
	return resolved, nil
}
func systemdQuote(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
func joinSystemdArguments(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		value = strings.ReplaceAll(value, "$", "$$")
		quoted[i] = systemdQuote(strings.ReplaceAll(value, "%", "%%"))
	}
	return strings.Join(quoted, " ")
}

func (h *Host) StopSiteHosting(ctx context.Context, site model.Site) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopSiteHostingLocked(ctx, site)
}

func (h *Host) stopSiteHostingLocked(ctx context.Context, site model.Site) error {
	if err := h.stopNodeRuntime(ctx, site); err != nil {
		return err
	}
	return h.removeSiteFTPAccess(ctx, site)
}
func (h *Host) DeleteSiteHosting(ctx context.Context, site model.Site) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deleteSiteHostingLocked(ctx, site)
}

func (h *Host) deleteSiteHostingLocked(ctx context.Context, site model.Site) error {
	if err := h.stopNodeRuntime(ctx, site); err != nil {
		return err
	}
	if err := h.removeSiteFTPAccess(ctx, site); err != nil {
		return err
	}
	unitPath := filepath.Join(h.HostingUnitRoot, "wpx-node-"+site.ID+".service")
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return h.Runner.Run(ctx, "/usr/bin/systemctl", "daemon-reload")
}

func (h *Host) stopNodeRuntime(ctx context.Context, site model.Site) error {
	if !safeHostingRoot(h.HostingUnitRoot) || h.Runner == nil {
		return errors.New("invalid Node lifecycle configuration")
	}
	unit := "wpx-node-" + site.ID + ".service"
	unitPath := filepath.Join(h.HostingUnitRoot, unit)
	if _, err := os.Lstat(unitPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, _, err := managedFileState(unitPath); err != nil {
		return err
	}
	return h.Runner.Run(ctx, "/usr/bin/systemctl", "disable", "--now", unit)
}
func safeHostingRoot(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && value != "/" && !strings.ContainsAny(value, " \t\r\n")
}
