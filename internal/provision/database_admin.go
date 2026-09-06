package provision

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	phpMyAdminVersion    = "5.2.3"
	phpMyAdminURL        = "https://files.phpmyadmin.net/phpMyAdmin/5.2.3/phpMyAdmin-5.2.3-all-languages.zip"
	phpMyAdminSHA256     = "2d2e13c735366d318425c78e4ee2cc8fc648d77faba3ddea2cd516e43885733f"
	maxPHPMyAdminArchive = 24 << 20
	maxPHPMyAdminFiles   = 160 << 20
	phpMyAdminPHPVersion = "8.4"
)

type PHPMyAdmin struct {
	Runner         Runner
	HTTP           *http.Client
	Root           string
	StateRoot      string
	TokenRoot      string
	PHPConfigRoot  string
	PHPRunRoot     string
	NginxAvailable string
	NginxEnabled   string
}

func DefaultPHPMyAdmin(runner Runner, _ string) *PHPMyAdmin {
	return &PHPMyAdmin{
		Runner: runner, HTTP: &http.Client{Timeout: 3 * time.Minute},
		Root: "/usr/local/lib/wpx/phpmyadmin", StateRoot: "/var/lib/wpx-phpmyadmin", TokenRoot: "/run/wpx-phpmyadmin",
		PHPConfigRoot: "/etc/php", PHPRunRoot: "/run/php", NginxAvailable: "/etc/nginx/sites-available", NginxEnabled: "/etc/nginx/sites-enabled",
	}
}

func (p *PHPMyAdmin) Ensure(ctx context.Context) error {
	if err := p.validate(); err != nil {
		return err
	}
	if err := (&AptPHPRuntime{Runner: p.Runner}).ensurePackages(ctx, phpMyAdminPHPVersion); err != nil {
		return err
	}
	identity, err := p.ensureIdentity(ctx)
	if err != nil {
		return err
	}
	for _, path := range []string{p.StateRoot, filepath.Join(p.StateRoot, "sessions"), filepath.Join(p.StateRoot, "tmp"), p.TokenRoot} {
		if err := ensureDirectory(path, 0770, identity); err != nil {
			return fmt.Errorf("prepare phpMyAdmin state: %w", err)
		}
	}
	if err := p.installApplication(ctx); err != nil {
		return err
	}
	if err := p.writeConfiguration(identity); err != nil {
		return err
	}
	for _, script := range []string{filepath.Join(p.Root, "config.inc.php"), filepath.Join(p.Root, "wpx-signon.php")} {
		if err := p.Runner.Run(ctx, "/usr/bin/php"+phpMyAdminPHPVersion, "-l", script); err != nil {
			return fmt.Errorf("validate phpMyAdmin PHP configuration: %w", err)
		}
	}
	poolPath := filepath.Join(p.PHPConfigRoot, phpMyAdminPHPVersion, "fpm", "pool.d", "wpx-phpmyadmin.conf")
	previousPool, poolExisted, err := managedFileStateWithMarker(poolPath, phpOwnershipMarker)
	if err != nil {
		return err
	}
	if err := atomicWrite(poolPath, []byte(p.renderPool(identity)), 0644); err != nil {
		return fmt.Errorf("write phpMyAdmin PHP pool: %w", err)
	}
	if err := p.Runner.Run(ctx, "/usr/sbin/php-fpm"+phpMyAdminPHPVersion, "-t"); err != nil {
		restoreManagedFile(poolPath, previousPool, poolExisted)
		return fmt.Errorf("validate phpMyAdmin PHP pool: %w", err)
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "php"+phpMyAdminPHPVersion+"-fpm.service"); err != nil {
		return err
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "php"+phpMyAdminPHPVersion+"-fpm.service"); err != nil {
		return err
	}
	available := filepath.Join(p.NginxAvailable, "wpx-phpmyadmin.conf")
	previousNginx, nginxExisted, err := managedFileStateWithMarker(available, ownershipMarker)
	if err != nil {
		return err
	}
	if err := atomicWrite(available, []byte(p.renderNginx()), 0644); err != nil {
		return fmt.Errorf("write phpMyAdmin Nginx configuration: %w", err)
	}
	enabled := filepath.Join(p.NginxEnabled, "wpx-phpmyadmin.conf")
	linkCreated, err := ensureSymlink(available, enabled)
	if err != nil {
		restoreManagedFile(available, previousNginx, nginxExisted)
		return err
	}
	if err := p.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		restorePHPMyAdminNginx(available, enabled, previousNginx, nginxExisted, linkCreated)
		return fmt.Errorf("validate phpMyAdmin Nginx configuration: %w", err)
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		restorePHPMyAdminNginx(available, enabled, previousNginx, nginxExisted, linkCreated)
		_ = p.Runner.Run(context.Background(), "/usr/sbin/nginx", "-t")
		_ = p.Runner.Run(context.Background(), "/usr/bin/systemctl", "reload", "nginx.service")
		return fmt.Errorf("reload Nginx for phpMyAdmin: %w", err)
	}
	return nil
}

func (p *PHPMyAdmin) CreateSignon(_ context.Context, credentials DatabaseCredentials) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	if credentials.Name == "" || credentials.User == "" || credentials.Password == "" || credentials.Host != "localhost" {
		return "", errors.New("database credentials are incomplete")
	}
	identity, err := user.Lookup("wpx-pma")
	if err != nil {
		return "", errors.New("phpMyAdmin is not installed")
	}
	uid, err := strconv.Atoi(identity.Uid)
	if err != nil {
		return "", err
	}
	gid, err := strconv.Atoi(identity.Gid)
	if err != nil {
		return "", err
	}
	if err := ensureDirectory(p.TokenRoot, 0770, Identity{Name: "wpx-pma", UID: uid, GID: gid}); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(p.TokenRoot)
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(p.TokenRoot, entry.Name()))
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	payload := struct {
		User      string `json:"user"`
		Password  string `json:"password"`
		ExpiresAt int64  `json:"expires_at"`
	}{credentials.User, credentials.Password, time.Now().Add(time.Minute).Unix()}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	path := filepath.Join(p.TokenRoot, token+".json")
	if err := atomicWrite(path, encoded, 0640); err != nil {
		return "", err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return "", err
	}
	return token, nil
}

func (p *PHPMyAdmin) validate() error {
	if p.Runner == nil || p.HTTP == nil {
		return errors.New("phpMyAdmin dependencies are incomplete")
	}
	for _, path := range []string{p.Root, p.StateRoot, p.TokenRoot, p.PHPConfigRoot, p.PHPRunRoot, p.NginxAvailable, p.NginxEnabled} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("phpMyAdmin path configuration is unsafe")
		}
	}
	return nil
}

func (p *PHPMyAdmin) ensureIdentity(ctx context.Context) (Identity, error) {
	account, err := user.Lookup("wpx-pma")
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return Identity{}, err
		}
		if err := p.Runner.Run(ctx, "/usr/sbin/useradd", "--system", "--user-group", "--home-dir", p.StateRoot, "--shell", "/usr/sbin/nologin", "wpx-pma"); err != nil {
			return Identity{}, fmt.Errorf("create phpMyAdmin identity: %w", err)
		}
		account, err = user.Lookup("wpx-pma")
		if err != nil {
			return Identity{}, err
		}
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return Identity{}, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Name: "wpx-pma", UID: uid, GID: gid}, nil
}

func (p *PHPMyAdmin) installApplication(ctx context.Context) error {
	marker := filepath.Join(p.Root, ".wpx-version")
	if current, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(current)) == phpMyAdminVersion {
		return nil
	} else if err == nil {
		return errors.New("a different managed phpMyAdmin version already exists; upgrade reconciliation is required")
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(p.Root); err == nil {
		return errors.New("refuse to replace an unrecognized phpMyAdmin directory")
	} else if !os.IsNotExist(err) {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, phpMyAdminURL, nil)
	if err != nil {
		return err
	}
	response, err := p.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("download phpMyAdmin %s: %w", phpMyAdminVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download phpMyAdmin %s: unexpected HTTP status %s", phpMyAdminVersion, response.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxPHPMyAdminArchive+1))
	if err != nil {
		return err
	}
	if len(archive) > maxPHPMyAdminArchive {
		return errors.New("phpMyAdmin archive exceeds size limit")
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != phpMyAdminSHA256 {
		return errors.New("phpMyAdmin SHA-256 verification failed")
	}
	return p.extractApplication(archive)
}

func (p *PHPMyAdmin) extractApplication(archive []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("open phpMyAdmin archive: %w", err)
	}
	parent := filepath.Dir(p.Root)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".phpmyadmin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	prefix := "phpMyAdmin-" + phpMyAdminVersion + "-all-languages/"
	var total uint64
	for _, entry := range reader.File {
		if !strings.HasPrefix(entry.Name, prefix) {
			return errors.New("phpMyAdmin archive has an unexpected layout")
		}
		relative := strings.TrimPrefix(entry.Name, prefix)
		if relative == "" || strings.HasPrefix(relative, "setup/") || relative == "setup" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(relative))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("phpMyAdmin archive contains an unsafe path")
		}
		target := filepath.Join(temporary, clean)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		total += entry.UncompressedSize64
		if total > maxPHPMyAdminFiles {
			return errors.New("phpMyAdmin extracted files exceed size limit")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			input.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		closeErr := output.Close()
		input.Close()
		if copyErr != nil || closeErr != nil || written != int64(entry.UncompressedSize64) {
			return errors.New("extract phpMyAdmin file")
		}
	}
	if err := atomicWrite(filepath.Join(temporary, ".wpx-version"), []byte(phpMyAdminVersion+"\n"), 0644); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0755); err != nil {
		return err
	}
	return os.Rename(temporary, p.Root)
}

func (p *PHPMyAdmin) writeConfiguration(identity Identity) error {
	secretPath := filepath.Join(p.Root, ".wpx-blowfish")
	secretBytes, err := os.ReadFile(secretPath)
	if os.IsNotExist(err) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		secretBytes = []byte(base64.RawURLEncoding.EncodeToString(raw))
		if err := atomicWrite(secretPath, secretBytes, 0640); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	secret := strings.TrimSpace(string(secretBytes))
	if len(secret) != 43 || strings.ContainsAny(secret, "'\"\\\r\n") {
		return errors.New("phpMyAdmin blowfish key is invalid")
	}
	if err := os.Chown(secretPath, 0, identity.GID); err != nil {
		return err
	}
	configPath := filepath.Join(p.Root, "config.inc.php")
	if err := atomicWrite(configPath, []byte(phpMyAdminConfig(secret, p.StateRoot)), 0640); err != nil {
		return err
	}
	if err := os.Chown(configPath, 0, identity.GID); err != nil {
		return err
	}
	signonPath := filepath.Join(p.Root, "wpx-signon.php")
	if err := atomicWrite(signonPath, []byte(phpMyAdminSignon(p.TokenRoot)), 0644); err != nil {
		return err
	}
	return nil
}

func phpMyAdminConfig(secret, stateRoot string) string {
	return "<?php\n" +
		"// Managed by WPX. Manual changes will be replaced.\n" +
		"$cfg['blowfish_secret'] = '" + secret + "';\n" +
		"$i = 1;\n" +
		"$cfg['Servers'][$i]['auth_type'] = 'signon';\n" +
		"$cfg['Servers'][$i]['SignonSession'] = 'WPXSignon';\n" +
		"$cfg['Servers'][$i]['SignonURL'] = '/phpmyadmin/wpx-signon.php';\n" +
		"$cfg['Servers'][$i]['LogoutURL'] = '/databases';\n" +
		"$cfg['Servers'][$i]['host'] = 'localhost';\n" +
		"$cfg['Servers'][$i]['connect_type'] = 'socket';\n" +
		"$cfg['Servers'][$i]['socket'] = '/run/mysqld/mysqld.sock';\n" +
		"$cfg['Servers'][$i]['verbose'] = 'Local MariaDB';\n" +
		"$cfg['TempDir'] = '" + filepath.Join(stateRoot, "tmp") + "';\n" +
		"$cfg['SessionSavePath'] = '" + filepath.Join(stateRoot, "sessions") + "';\n" +
		"$forwardedHost = $_SERVER['HTTP_X_FORWARDED_HOST'] ?? '';\n" +
		"if (preg_match('/\\A[a-zA-Z0-9.-]+(?::[0-9]{1,5})?\\z/', $forwardedHost)) { $cfg['PmaAbsoluteUri'] = 'https://' . $forwardedHost . '/phpmyadmin/'; }\n"
}

func phpMyAdminSignon(tokenRoot string) string {
	return `<?php
// Managed by WPX. A broker-created token is valid for one minute and one read.
$token = $_GET['token'] ?? '';
if (!preg_match('/\A[A-Za-z0-9_-]{43}\z/', $token)) { http_response_code(400); exit('Invalid sign-in request.'); }
$path = '` + tokenRoot + `/' . $token . '.json';
$raw = @file_get_contents($path);
if ($raw === false || !@unlink($path)) { http_response_code(403); exit('This sign-in request expired or was already used.'); }
$credentials = json_decode($raw, true);
if (!is_array($credentials) || ($credentials['expires_at'] ?? 0) < time()) { http_response_code(403); exit('This sign-in request expired.'); }
session_name('WPXSignon');
session_start();
$_SESSION['PMA_single_signon_user'] = $credentials['user'];
$_SESSION['PMA_single_signon_password'] = $credentials['password'];
$_SESSION['PMA_single_signon_host'] = 'localhost';
session_write_close();
header('Cache-Control: no-store');
header('Location: /phpmyadmin/index.php', true, 303);
`
}

func (p *PHPMyAdmin) renderPool(identity Identity) string {
	socket := filepath.Join(p.PHPRunRoot, "wpx-phpmyadmin.sock")
	return phpOwnershipMarker + "[wpx-phpmyadmin]\n" +
		"user = " + identity.Name + "\n" + "group = " + identity.Name + "\n" +
		"listen = " + socket + "\nlisten.owner = www-data\nlisten.group = www-data\nlisten.mode = 0660\n" +
		"pm = ondemand\npm.max_children = 3\npm.process_idle_timeout = 10s\npm.max_requests = 300\n" +
		"clear_env = yes\ncatch_workers_output = yes\n" +
		"php_admin_value[upload_tmp_dir] = " + filepath.Join(p.StateRoot, "tmp") + "\n" +
		"php_admin_value[session.save_path] = " + filepath.Join(p.StateRoot, "sessions") + "\n" +
		"php_admin_flag[session.cookie_secure] = on\n" +
		"php_admin_value[session.cookie_path] = /phpmyadmin/\n"
}

func (p *PHPMyAdmin) renderNginx() string {
	return ownershipMarker + "server {\n" +
		"    listen 127.0.0.1:9081;\n    server_name localhost;\n    access_log off;\n    error_log /var/log/nginx/wpx-phpmyadmin-error.log warn;\n" +
		"    root " + p.Root + ";\n    index index.php;\n" +
		"    location / { try_files $uri $uri/ /index.php?$args; }\n" +
		"    location ~ \\.php$ {\n        try_files $uri =404;\n        include fastcgi_params;\n        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n        fastcgi_pass unix:" + filepath.Join(p.PHPRunRoot, "wpx-phpmyadmin.sock") + ";\n    }\n" +
		"    location ~ /\\. { deny all; }\n}\n"
}

func ensureSymlink(target, link string) (bool, error) {
	if current, err := os.Readlink(link); err == nil {
		if current == target {
			return false, nil
		}
		return false, errors.New("refuse to replace an unmanaged Nginx link")
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Symlink(target, link); err != nil {
		return false, err
	}
	return true, nil
}

func restoreManagedFile(path string, previous []byte, existed bool) {
	if existed {
		_ = atomicWrite(path, previous, 0644)
	} else {
		_ = os.Remove(path)
	}
}

func restorePHPMyAdminNginx(available, enabled string, previous []byte, existed, linkCreated bool) {
	if linkCreated {
		_ = os.Remove(enabled)
	}
	restoreManagedFile(available, previous, existed)
}
