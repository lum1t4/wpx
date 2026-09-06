//go:build linux

package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

func (h *Host) MagicLogin(ctx context.Context, site model.Site) (string, time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress {
		return "", time.Time{}, errors.New("magic login requires a valid WordPress site")
	}
	if err := h.validate(); err != nil {
		return "", time.Time{}, err
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return "", time.Time{}, err
	}
	// Open the public directory without following any symbolic link in the path.
	// A compromised site user can mutate content concurrently; openat2 keeps the
	// root broker from being tricked into writing outside the site's directory.
	dirFD, err := unix.Openat2(unix.AT_FDCWD, publicDir, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("open WordPress public directory safely: %w", err)
	}
	defer unix.Close(dirFD)
	var core unix.Stat_t
	if err := unix.Fstatat(dirFD, "wp-load.php", &core, unix.AT_SYMLINK_NOFOLLOW); err != nil || core.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", time.Time{}, errors.New("WordPress core is not installed")
	}
	// Deletion may have completed after this request was authorized. Never
	// recreate its Unix identity unless the real WordPress tree still exists.
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return "", time.Time{}, err
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	name := "wpx-login-" + token + ".php"
	expires := time.Now().UTC().Add(90 * time.Second)
	script := magicLoginScript(expires)
	if err := writeFileAt(dirFD, name, []byte(script), identity); err != nil {
		return "", time.Time{}, fmt.Errorf("write one-time WordPress login: %w", err)
	}
	scheme := "http"
	if site.TLSStatus == "active" {
		// The filename is a bearer credential. Start on HTTPS when provisioned
		// so an HTTP redirect cannot expose it on the first request.
		scheme = "https"
	}
	return scheme + "://" + site.Domain + "/" + name, expires, nil
}

func writeFileAt(dirFD int, name string, content []byte, identity Identity) error {
	fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return errors.New("create file handle")
	}
	remove := true
	defer func() {
		file.Close()
		if remove {
			_ = unix.Unlinkat(dirFD, name, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chown(identity.UID, identity.GID); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func magicLoginScript(expires time.Time) string {
	return `<?php
// Managed by WPX. This file deletes itself before authenticating and expires
// even if it is never opened.
if (time() > ` + fmt.Sprintf("%d", expires.Unix()) + `) {
    @unlink(__FILE__);
    http_response_code(410);
    exit('This login link has expired.');
}
if (!@unlink(__FILE__)) {
    http_response_code(410);
    exit('This login link has already been used.');
}
define('WP_USE_THEMES', false);
require __DIR__ . '/wp-load.php';
$user_id = 0;
if (is_multisite()) {
    $super_administrators = get_super_admins();
    $administrator = $super_administrators ? get_user_by('login', $super_administrators[0]) : false;
    $user_id = $administrator ? (int) $administrator->ID : 0;
} else {
    $administrators = get_users([
        'role' => 'administrator',
        'number' => 1,
        'orderby' => 'ID',
        'order' => 'ASC',
        'fields' => 'ID',
    ]);
    $user_id = $administrators ? (int) $administrators[0] : 0;
}
if (!$user_id) {
    wp_die('No active WordPress administrator exists.', 403);
}
wp_set_current_user($user_id);
wp_set_auth_cookie($user_id, false, is_ssl());
wp_safe_redirect(admin_url());
exit;
`
}
