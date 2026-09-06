package panelaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// proxySnapshot covers exactly the two filesystem entries WPX owns. Certificate
// files are deliberately outside the transaction: certbot owns their renewal
// state, and a successfully issued certificate remains useful after a rollback.
type proxySnapshot struct {
	available  string
	enabled    string
	content    []byte
	mode       os.FileMode
	existed    bool
	linkTarget string
	changed    bool
}

func snapshotProxy(options Options) (*proxySnapshot, error) {
	snapshot := &proxySnapshot{
		available: filepath.Join(options.NginxAvailable, "wpx-panel.conf"),
		enabled:   filepath.Join(options.NginxEnabled, "wpx-panel.conf"),
	}
	info, err := os.Lstat(snapshot.available)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refuse to replace non-regular Nginx configuration %s", snapshot.available)
		}
		snapshot.content, err = os.ReadFile(snapshot.available)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(string(snapshot.content), ownershipMarker) {
			return nil, fmt.Errorf("refuse to replace unmanaged Nginx configuration %s", snapshot.available)
		}
		snapshot.existed, snapshot.mode = true, info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	target, err := os.Readlink(snapshot.enabled)
	if err == nil {
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(snapshot.enabled), resolved)
		}
		if filepath.Clean(resolved) != snapshot.available || !snapshot.existed {
			return nil, fmt.Errorf("refuse to replace unexpected Nginx link %s", snapshot.enabled)
		}
		snapshot.linkTarget = target
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("refuse to replace non-link %s", snapshot.enabled)
	}
	return snapshot, nil
}

func (snapshot *proxySnapshot) restore(ctx context.Context, options Options) error {
	var failures []error
	if snapshot.existed {
		if err := writeOwned(snapshot.available, snapshot.content); err != nil {
			failures = append(failures, err)
		} else if err := os.Chmod(snapshot.available, snapshot.mode); err != nil {
			failures = append(failures, err)
		}
	} else if err := os.Remove(snapshot.available); err != nil && !os.IsNotExist(err) {
		failures = append(failures, err)
	}
	if err := os.Remove(snapshot.enabled); err != nil && !os.IsNotExist(err) {
		failures = append(failures, err)
	} else if snapshot.linkTarget != "" {
		if err := os.Symlink(snapshot.linkTarget, snapshot.enabled); err != nil {
			failures = append(failures, err)
		}
	}
	// Do not reload a partially restored configuration. Report every filesystem
	// failure so the operator can distinguish the original error from recovery.
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return reloadNginx(ctx, options.Runner)
}

func reloadNginx(ctx context.Context, runner Runner) error {
	if err := runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return fmt.Errorf("validate Nginx configuration: %w", err)
	}
	if err := runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return fmt.Errorf("reload Nginx: %w", err)
	}
	return nil
}
