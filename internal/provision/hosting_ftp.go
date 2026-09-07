package provision

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) ApplyFTPUser(ctx context.Context, site model.Site, ftp model.FTPUser) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if site.Status != "active" {
		return errors.New("FTP access requires an active site")
	}
	if err := model.ValidateFTPUser(ftp); err != nil {
		return err
	}
	if ftp.SiteID != site.ID {
		return errors.New("FTP user does not match site")
	}
	if h.Runner == nil || h.Identities == nil || !safeHostingRoot(h.SiteRoot) || !safeHostingRoot(h.DataRoot) || !safeHostingRoot(h.ProFTPDConfigRoot) {
		return errors.New("FTP dependencies are unavailable")
	}
	if err := h.Runner.Run(ctx, "/usr/bin/apt-get", "install", "-y", "--no-install-recommends", "proftpd-core", "proftpd-mod-crypto"); err != nil {
		return fmt.Errorf("install ProFTPD: %w", err)
	}
	siteRoot := filepath.Join(h.SiteRoot, site.ID)
	identity, err := h.Identities.Ensure(ctx, site, siteRoot)
	if err != nil {
		return fmt.Errorf("load site identity: %w", err)
	}
	if err := requireRegularSecret(h.PanelTLSCertPath); err != nil {
		return fmt.Errorf("inspect panel TLS certificate: %w", err)
	}
	if err := requireRegularSecret(h.PanelTLSKeyPath); err != nil {
		return fmt.Errorf("inspect panel TLS key: %w", err)
	}
	authPath := filepath.Join(h.DataRoot, "proftpd.passwd")
	configPath := filepath.Join(h.ProFTPDConfigRoot, "wpx.conf")
	previousAuth, authExisted, err := managedRegularState(authPath)
	if err != nil {
		return fmt.Errorf("inspect ProFTPD password file: %w", err)
	}
	previousConfig, configExisted, err := managedRegularState(configPath)
	if err != nil {
		return fmt.Errorf("inspect ProFTPD configuration: %w", err)
	}
	line := strings.Join([]string{ftp.Username, ftp.PasswordHash, strconv.Itoa(identity.UID), strconv.Itoa(identity.GID), "", filepath.Join(siteRoot, "public"), "/usr/sbin/nologin"}, ":")
	if err := upsertFTPAuth(authPath, ftp.Username, line); err != nil {
		return err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/chown", "root:root", authPath); err != nil {
		restoreFTPAuth(authPath, previousAuth, authExisted)
		return fmt.Errorf("protect ProFTPD password file: %w", err)
	}
	config := proFTPDConfig(authPath, h.PanelTLSCertPath, h.PanelTLSKeyPath)
	if err := os.MkdirAll(h.ProFTPDConfigRoot, 0755); err != nil {
		restoreFTPAuth(authPath, previousAuth, authExisted)
		return err
	}
	if err := atomicWrite(configPath, []byte(config), 0644); err != nil {
		restoreFTPAuth(authPath, previousAuth, authExisted)
		return err
	}
	rollback := func() {
		restoreFTPAuth(authPath, previousAuth, authExisted)
		restoreManagedFile(configPath, previousConfig, configExisted)
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/proftpd", "-t"); err != nil {
		rollback()
		return fmt.Errorf("validate ProFTPD configuration: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "proftpd.service"); err != nil {
		rollback()
		_ = h.Runner.Run(context.Background(), "/usr/sbin/proftpd", "-t")
		_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "reload", "proftpd.service")
		return fmt.Errorf("start ProFTPD: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "proftpd.service"); err != nil {
		rollback()
		_ = h.Runner.Run(context.Background(), "/usr/sbin/proftpd", "-t")
		_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "reload", "proftpd.service")
		return fmt.Errorf("reload ProFTPD: %w", err)
	}
	return nil
}

func proFTPDConfig(auth, cert, key string) string {
	return ownershipMarker + "LoadModule mod_tls.c\nServerName \"WPX FTP\"\nDefaultRoot ~\nRequireValidShell off\nAuthOrder mod_auth_file.c\nAuthUserFile " + auth + "\nUseIPv6 off\nPassivePorts 49152 49252\n<IfModule mod_tls.c>\n  TLSEngine on\n  TLSProtocol TLSv1.2 TLSv1.3\n  TLSRequired on\n  TLSRSACertificateFile " + cert + "\n  TLSRSACertificateKeyFile " + key + "\n</IfModule>\n<IfModule !mod_tls.c>\n  <Limit LOGIN>\n    DenyAll\n  </Limit>\n</IfModule>\n"
}

func upsertFTPAuth(path, username, line string) error {
	previous, existed, err := managedRegularState(path)
	if err != nil {
		return err
	}
	lines := []string{strings.TrimSuffix(ownershipMarker, "\n")}
	if existed {
		scanner := bufio.NewScanner(strings.NewReader(string(previous)))
		for scanner.Scan() {
			if scanner.Text() != strings.TrimSuffix(ownershipMarker, "\n") && !strings.HasPrefix(scanner.Text(), username+":") {
				lines = append(lines, scanner.Text())
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}
	lines = append(lines, line)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return atomicWrite(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func (h *Host) RemoveSiteFTPAccess(ctx context.Context, site model.Site) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.removeSiteFTPAccess(ctx, site)
}

func (h *Host) DeleteFTPUser(ctx context.Context, site model.Site, ftp model.FTPUser) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := model.ValidateFTPUser(ftp); err != nil {
		return err
	}
	if ftp.SiteID != site.ID {
		return errors.New("FTP user does not match site")
	}
	if h.Runner == nil || !safeHostingRoot(h.SiteRoot) || !safeHostingRoot(h.DataRoot) {
		return errors.New("FTP dependencies are unavailable")
	}
	path := filepath.Join(h.DataRoot, "proftpd.passwd")
	content, existed, err := managedRegularState(path)
	if err != nil || !existed {
		return err
	}
	home := filepath.Join(h.SiteRoot, site.ID, "public")
	kept := []string{strings.TrimSuffix(ownershipMarker, "\n")}
	removed := false
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == strings.TrimSuffix(ownershipMarker, "\n") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) > 5 && parts[0] == ftp.Username {
			if filepath.Clean(parts[5]) != home {
				return errors.New("FTP user belongs to another site")
			}
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if removed {
		if err := atomicWrite(path, []byte(strings.Join(kept, "\n")+"\n"), 0600); err != nil {
			return err
		}
		if err := h.Runner.Run(ctx, "/usr/bin/chown", "root:root", path); err != nil {
			return err
		}
	}
	return h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "proftpd.service")
}

func (h *Host) removeSiteFTPAccess(ctx context.Context, site model.Site) error {
	path := filepath.Join(h.DataRoot, "proftpd.passwd")
	content, existed, err := managedRegularState(path)
	if err != nil || !existed {
		return err
	}
	home := filepath.Join(h.SiteRoot, site.ID, "public")
	kept := []string{strings.TrimSuffix(ownershipMarker, "\n")}
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		parts := strings.Split(line, ":")
		if line != strings.TrimSuffix(ownershipMarker, "\n") && !(len(parts) > 5 && filepath.Clean(parts[5]) == home) {
			kept = append(kept, line)
		}
	}
	if err := atomicWrite(path, []byte(strings.Join(kept, "\n")+"\n"), 0600); err != nil {
		return err
	}
	if err := h.Runner.Run(ctx, "/usr/bin/chown", "root:root", path); err != nil {
		return err
	}
	return h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "proftpd.service")
}

func requireRegularSecret(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a regular file")
	}
	return nil
}

func managedRegularState(path string) ([]byte, bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("refuse non-regular managed file")
	}
	return managedFileState(path)
}

func restoreFTPAuth(path string, previous []byte, existed bool) {
	if existed {
		_ = atomicWrite(path, previous, 0600)
	} else {
		_ = os.Remove(path)
	}
}
