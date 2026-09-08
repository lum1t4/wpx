//go:build linux

package provision

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

const (
	magicLoginHandlerName = "wpx-login-handler.php"
	magicLoginTokenDir    = "wpx-login"
	magicLoginTTL         = 90 * time.Second
	magicLoginMaxScan     = 4096
	magicLoginMaxCleanup  = 256
)

var (
	legacyMagicLoginName = regexp.MustCompile(`^wpx-login-[a-f0-9]{64}\.php$`)
	magicLoginStateName  = regexp.MustCompile(`^[a-f0-9]{64}\.(?:token|used)$`)
)

func (h *Host) MagicLogin(ctx context.Context, site model.Site) (string, time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress || site.Status != "active" {
		return "", time.Time{}, errors.New("magic login requires an active WordPress site")
	}
	if err := h.validate(); err != nil {
		return "", time.Time{}, err
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return "", time.Time{}, err
	}
	publicFD, err := unix.Openat2(unix.AT_FDCWD, publicDir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("open WordPress public directory safely: %w", err)
	}
	defer unix.Close(publicFD)
	var core unix.Stat_t
	if err := unix.Fstatat(publicFD, "wp-load.php", &core, unix.AT_SYMLINK_NOFOLLOW); err != nil || core.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", time.Time{}, errors.New("WordPress core is not installed")
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	if err := cleanupLegacyMagicLoginFiles(publicFD, now); err != nil {
		return "", time.Time{}, err
	}
	if err := ensureMagicLoginHandler(publicFD, identity); err != nil {
		return "", time.Time{}, err
	}
	tokenFD, err := openMagicLoginTokenDirectory(siteDir, identity)
	if err != nil {
		return "", time.Time{}, err
	}
	defer unix.Close(tokenFD)
	if err := cleanupMagicLoginState(tokenFD, now); err != nil {
		return "", time.Time{}, err
	}
	token, expires, err := createMagicLoginCapability(tokenFD, identity, now)
	if err != nil {
		return "", time.Time{}, err
	}
	scheme := "http"
	if site.TLSStatus == "active" {
		scheme = "https"
	}
	return scheme + "://" + site.Domain + "/" + magicLoginHandlerName + "#" + token, expires, nil
}

func ensureMagicLoginHandler(publicFD int, identity Identity) error {
	content := []byte(magicLoginHandlerScript())
	fd, err := unix.Openat2(publicFD, magicLoginHandlerName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		if err := installMagicLoginHandler(publicFD, content, identity); err != nil {
			return fmt.Errorf("write stable WordPress login handler: %w", err)
		}
		return nil
	}
	if err != nil {
		return errors.New("stable WordPress login handler is not a trusted managed file")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != int64(len(content)) {
		unix.Close(fd)
		return errors.New("stable WordPress login handler is not a trusted managed file")
	}
	file := os.NewFile(uintptr(fd), magicLoginHandlerName)
	if file == nil {
		unix.Close(fd)
		return errors.New("open stable WordPress login handler")
	}
	defer file.Close()
	existing, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	if err != nil || string(existing) != string(content) {
		return errors.New("stable WordPress login handler was modified outside WPX")
	}
	if err := file.Chmod(0600); err != nil {
		return err
	}
	return file.Chown(identity.UID, identity.GID)
}

func installMagicLoginHandler(publicFD int, content []byte, identity Identity) error {
	for range 4 {
		temporary, err := randomTemporaryName()
		if err != nil {
			return err
		}
		if err := createFileAt(publicFD, temporary, content, 0600, identity); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				_ = unix.Unlinkat(publicFD, temporary, 0)
			}
		}()
		if err := unix.Renameat2(publicFD, temporary, publicFD, magicLoginHandlerName, unix.RENAME_NOREPLACE); err != nil {
			return err
		}
		published = true
		return unix.Fsync(publicFD)
	}
	return errors.New("could not allocate a temporary login handler")
}

func openMagicLoginTokenDirectory(siteDir string, identity Identity) (int, error) {
	siteFD, err := unix.Openat2(unix.AT_FDCWD, siteDir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, fmt.Errorf("open WordPress site directory safely: %w", err)
	}
	defer unix.Close(siteFD)
	tmpFD, err := openOrCreateDirectoryAt(siteFD, "tmp", 0700)
	if err != nil {
		return -1, errors.New("private site directory is not trusted")
	}
	defer unix.Close(tmpFD)
	if err := unix.Fchmod(tmpFD, 0700); err != nil {
		return -1, err
	}
	if err := unix.Fchown(tmpFD, identity.UID, identity.GID); err != nil {
		return -1, err
	}
	loginFD, err := openOrCreateDirectoryAt(tmpFD, magicLoginTokenDir, 0700)
	if err != nil {
		return -1, errors.New("private login capability directory is not trusted")
	}
	if err := unix.Fchmod(loginFD, 0700); err != nil {
		unix.Close(loginFD)
		return -1, err
	}
	if err := unix.Fchown(loginFD, identity.UID, identity.GID); err != nil {
		unix.Close(loginFD)
		return -1, err
	}
	return loginFD, nil
}

func openOrCreateDirectoryAt(parentFD int, name string, mode uint32) (int, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(parentFD, name, mode); err != nil {
			return -1, err
		}
		return unix.Openat2(parentFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	}
	return fd, err
}

func createMagicLoginCapability(dirFD int, identity Identity, now time.Time) (string, time.Time, error) {
	expires := now.Add(magicLoginTTL).Truncate(time.Second)
	content := []byte("v1\n" + strconv.FormatInt(expires.Unix(), 10) + "\n")
	for range 4 {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return "", time.Time{}, err
		}
		token := hex.EncodeToString(random)
		digest := sha256.Sum256([]byte(token))
		name := hex.EncodeToString(digest[:]) + ".token"
		if err := writeFileAt(dirFD, name, content, identity); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return "", time.Time{}, fmt.Errorf("write private login capability: %w", err)
		}
		if err := unix.Fsync(dirFD); err != nil {
			_ = unix.Unlinkat(dirFD, name, 0)
			return "", time.Time{}, fmt.Errorf("persist private login capability: %w", err)
		}
		return token, expires, nil
	}
	return "", time.Time{}, errors.New("could not allocate a unique login capability")
}

func cleanupMagicLoginState(dirFD int, now time.Time) error {
	names, err := directoryNamesAt(dirFD, magicLoginMaxScan)
	if err != nil {
		return err
	}
	removed := 0
	for _, name := range names {
		if removed >= magicLoginMaxCleanup || !magicLoginStateName.MatchString(name) {
			continue
		}
		content, trusted := smallRegularFileAt(dirFD, name, 32)
		if !trusted {
			continue
		}
		expires, valid := parseMagicLoginState(content)
		remove := valid && !now.Before(expires)
		if remove && unix.Unlinkat(dirFD, name, 0) == nil {
			removed++
		}
	}
	return nil
}

func cleanupLegacyMagicLoginFiles(publicFD int, now time.Time) error {
	names, err := directoryNamesAt(publicFD, magicLoginMaxScan)
	if err != nil {
		return err
	}
	removed := 0
	for _, name := range names {
		if removed >= magicLoginMaxCleanup || !legacyMagicLoginName.MatchString(name) {
			continue
		}
		content, trusted := smallRegularFileAt(publicFD, name, 8192)
		if !trusted {
			continue
		}
		expires, managed := legacyMagicLoginExpiry(content)
		if managed && !now.Before(expires) && unix.Unlinkat(publicFD, name, 0) == nil {
			removed++
		}
	}
	return nil
}

func directoryNamesAt(dirFD, limit int) ([]string, error) {
	duplicate, err := unix.Dup(dirFD)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "managed-directory")
	if directory == nil {
		unix.Close(duplicate)
		return nil, errors.New("open managed directory")
	}
	defer directory.Close()
	names, err := directory.Readdirnames(limit)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return names, err
}

func smallRegularFileAt(dirFD int, name string, limit int64) ([]byte, bool) {
	fd, err := unix.Openat2(dirFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, false
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > limit {
		unix.Close(fd)
		return nil, false
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, false
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	return content, err == nil && int64(len(content)) <= limit
}

func parseMagicLoginState(content []byte) (time.Time, bool) {
	parts := strings.Split(string(content), "\n")
	if len(parts) != 3 || parts[0] != "v1" || parts[2] != "" || len(parts[1]) < 10 || len(parts[1]) > 11 {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

func legacyMagicLoginExpiry(content []byte) (time.Time, bool) {
	prefix := "<?php\n// Managed by WPX. This file deletes itself before authenticating and expires\n// even if it is never opened.\nif (time() > "
	if !strings.HasPrefix(string(content), prefix) {
		return time.Time{}, false
	}
	remainder := string(content[len(prefix):])
	separator := strings.Index(remainder, ") {")
	if separator < 1 || separator > 11 {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(remainder[:separator], 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	expires := time.Unix(seconds, 0).UTC()
	return expires, string(content) == magicLoginScript(expires)
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

func magicLoginHandlerScript() string {
	return `<?php
// Managed by WPX. Stable WordPress login handler v1.
header('Cache-Control: no-store, no-cache, must-revalidate, max-age=0');
header('Pragma: no-cache');
header('Referrer-Policy: no-referrer');
header('X-Robots-Tag: noindex, nofollow, noarchive');
header('X-Content-Type-Options: nosniff');
header("Content-Security-Policy: default-src 'none'; script-src 'nonce-" . ($nonce = base64_encode(random_bytes(18))) . "'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'");

function wpx_login_unavailable(int $status): void {
    http_response_code($status);
    exit('This WordPress login link is unavailable. Request a new link from WPX.');
}

if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
    header('Content-Type: text/html; charset=UTF-8');
    ?><!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Opening WordPress</title></head><body><p id="status">Opening WordPress administration…</p><noscript>JavaScript is required to use this one-time WordPress login link. Return to WPX and open it in a browser with JavaScript enabled.</noscript><script nonce="<?php echo htmlspecialchars($nonce, ENT_QUOTES, 'UTF-8'); ?>">
const token = window.location.hash.slice(1);
window.history.replaceState(null, '', window.location.pathname);
if (!/^[a-f0-9]{64}$/.test(token)) {
  document.getElementById('status').textContent = 'This WordPress login link is unavailable. Request a new link from WPX.';
} else {
  const form = document.createElement('form');
  form.method = 'post';
  form.action = window.location.pathname;
  const input = document.createElement('input');
  input.type = 'hidden'; input.name = 'token'; input.value = token;
  form.appendChild(input); document.body.appendChild(form); form.submit();
}
</script></body></html><?php
    exit;
}

$length = $_SERVER['CONTENT_LENGTH'] ?? '';
if (!is_string($length) || !preg_match('/\A[0-9]{1,3}\z/D', $length) || (int) $length < 1 || (int) $length > 256) {
    wpx_login_unavailable(400);
}
$token = $_POST['token'] ?? null;
if (!is_string($token) || !preg_match('/\A[a-f0-9]{64}\z/D', $token)) {
    wpx_login_unavailable(410);
}
$directory = dirname(__DIR__) . '/tmp/wpx-login';
$name = hash('sha256', $token);
$pending = $directory . '/' . $name . '.token';
$claimed = $directory . '/' . $name . '.used';
if (!@rename($pending, $claimed)) {
    wpx_login_unavailable(410);
}
$stat = @lstat($claimed);
$content = $stat && (($stat['mode'] & 0170000) === 0100000) && (($stat['mode'] & 0777) === 0600) && $stat['nlink'] === 1 && @fileowner($claimed) === @fileowner(__FILE__)
    ? @file_get_contents($claimed, false, null, 0, 32) : false;
@unlink($claimed);
if (!is_string($content) || !preg_match('/\Av1\n([0-9]{10,11})\n\z/D', $content, $match) || time() >= (int) $match[1]) {
    wpx_login_unavailable(410);
}

define('WP_USE_THEMES', false);
require __DIR__ . '/wp-load.php';
$user_id = 0;
if (is_multisite()) {
    $super_administrators = get_super_admins();
    $administrator = $super_administrators ? get_user_by('login', $super_administrators[0]) : false;
    $user_id = $administrator ? (int) $administrator->ID : 0;
} else {
    $administrators = get_users(['role' => 'administrator', 'number' => 1, 'orderby' => 'ID', 'order' => 'ASC', 'fields' => 'ID']);
    $user_id = $administrators ? (int) $administrators[0] : 0;
}
if (!$user_id) { wp_die('No active WordPress administrator exists.', 403); }
wp_set_current_user($user_id);
wp_set_auth_cookie($user_id, false, is_ssl());
wp_safe_redirect(admin_url());
exit;
`
}

// magicLoginScript reconstructs the exact pre-alpha19 generated file. It is
// retained only to recognize and safely remove expired legacy handlers.
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
