package provision

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

type WPCLI struct {
	Runner Runner
	Path   string
}

func (w *WPCLI) Ensure(ctx context.Context, site model.Site, identity Identity, publicDir string, database DatabaseCredentials) error {
	if w.Runner == nil || !filepath.IsAbs(w.Path) {
		return errors.New("invalid WP-CLI configuration")
	}
	if _, err := os.Stat(w.Path); err != nil {
		return fmt.Errorf("inspect WP-CLI: %w", err)
	}
	php := "/usr/bin/php" + site.PHPVersion
	base := []string{"--user", identity.Name, "--", php, w.Path, "--path=" + publicDir, "--no-color"}
	run := func(args ...string) error {
		return w.Runner.Run(ctx, "/usr/sbin/runuser", append(base, args...)...)
	}
	if _, err := os.Stat(filepath.Join(publicDir, "wp-load.php")); os.IsNotExist(err) {
		if err := run("core", "download", "--locale=en_US"); err != nil {
			return fmt.Errorf("download WordPress core: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect WordPress core: %w", err)
	}
	configPath := filepath.Join(publicDir, "wp-config.php")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := run("config", "create", "--dbname="+database.Name, "--dbuser="+database.User, "--dbpass="+database.Password, "--dbhost="+database.Host, "--skip-check"); err != nil {
			return fmt.Errorf("create WordPress configuration: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect WordPress configuration: %w", err)
	}
	if err := protectWordPressConfig(configPath, identity); err != nil {
		return err
	}
	if err := run("core", "is-installed"); err != nil {
		password, err := randomPassword()
		if err != nil {
			return err
		}
		installCommand := []string{
			"core", "install", "--url=http://" + site.Domain, "--title=" + site.Domain,
			"--admin_user=wpxadmin", "--admin_password=" + password,
			"--admin_email=admin@" + site.Domain, "--skip-email",
		}
		if site.WordPressMultisite != model.MultisiteDisabled {
			installCommand[1] = "multisite-install"
			if site.WordPressMultisite == model.MultisiteSubdomains {
				installCommand = append(installCommand, "--subdomains")
			}
		}
		if err := run(installCommand...); err != nil {
			return fmt.Errorf("install WordPress: %w", err)
		}
		if err := run("rewrite", "structure", "/%postname%/", "--hard"); err != nil {
			return fmt.Errorf("configure WordPress permalinks: %w", err)
		}
	}
	return w.ApplyRedis(ctx, site, identity, publicDir, site.RedisEnabled)
}

func protectWordPressConfig(path string, identity Identity) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open WordPress configuration safely: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("WordPress configuration is not a regular file")
	}
	if err := file.Chown(identity.UID, identity.GID); err != nil {
		return fmt.Errorf("own WordPress configuration: %w", err)
	}
	if err := file.Chmod(0640); err != nil {
		return fmt.Errorf("protect WordPress configuration: %w", err)
	}
	return nil
}

func (w *WPCLI) ApplyRedis(ctx context.Context, site model.Site, identity Identity, publicDir string, enabled bool) error {
	run := func(args ...string) error {
		base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, w.Path, "--path=" + publicDir, "--no-color"}
		return w.Runner.Run(ctx, "/usr/sbin/runuser", append(base, args...)...)
	}
	if enabled {
		if err := run("plugin", "install", "redis-cache", "--activate"); err != nil {
			return fmt.Errorf("install Redis object-cache integration: %w", err)
		}
		settings := [][]string{
			{"config", "set", "WP_REDIS_HOST", "127.0.0.1", "--type=constant"},
			{"config", "set", "WP_REDIS_PORT", "6379", "--type=constant", "--raw"},
			{"config", "set", "WP_REDIS_PREFIX", "wpx:" + site.ID + ":", "--type=constant"},
		}
		for _, arguments := range settings {
			if err := run(arguments...); err != nil {
				return fmt.Errorf("configure Redis object cache: %w", err)
			}
		}
		if err := run("redis", "enable"); err != nil {
			return fmt.Errorf("enable Redis object cache: %w", err)
		}
		return nil
	}
	pluginDirectory := filepath.Join(publicDir, "wp-content", "plugins", "redis-cache")
	dropIn := filepath.Join(publicDir, "wp-content", "object-cache.php")
	pluginInfo, pluginErr := os.Lstat(pluginDirectory)
	dropInInfo, dropInErr := os.Lstat(dropIn)
	if os.IsNotExist(pluginErr) && os.IsNotExist(dropInErr) {
		return nil
	}
	if pluginErr == nil && (!pluginInfo.IsDir() || pluginInfo.Mode()&os.ModeSymlink != 0) {
		return errors.New("Redis plugin path is not a trusted directory")
	}
	if pluginErr != nil && !os.IsNotExist(pluginErr) {
		return fmt.Errorf("inspect Redis plugin: %w", pluginErr)
	}
	if dropInErr == nil && (!dropInInfo.Mode().IsRegular() || dropInInfo.Mode()&os.ModeSymlink != 0) {
		return errors.New("Redis object-cache drop-in is not a trusted file")
	}
	if dropInErr != nil && !os.IsNotExist(dropInErr) {
		return fmt.Errorf("inspect Redis object-cache drop-in: %w", dropInErr)
	}
	if pluginErr != nil {
		return errors.New("Redis drop-in exists but its integration plugin is missing")
	}
	if os.IsNotExist(dropInErr) {
		if err := run("plugin", "deactivate", "redis-cache"); err != nil {
			return fmt.Errorf("deactivate Redis integration: %w", err)
		}
		return nil
	}
	// The plugin's `wp redis` command is registered only while the plugin is
	// active. Activating first makes disable retries converge even if a previous
	// attempt already deactivated it after removing the drop-in.
	if err := run("plugin", "activate", "redis-cache"); err != nil {
		return fmt.Errorf("activate Redis integration for cleanup: %w", err)
	}
	if err := run("redis", "disable"); err != nil {
		return fmt.Errorf("disable Redis object cache: %w", err)
	}
	if err := run("plugin", "deactivate", "redis-cache"); err != nil {
		return fmt.Errorf("deactivate Redis integration: %w", err)
	}
	return nil
}

func (w *WPCLI) EnableHTTPS(ctx context.Context, site model.Site, identity Identity, publicDir string) error {
	if w.Runner == nil || !filepath.IsAbs(w.Path) {
		return errors.New("invalid WP-CLI configuration")
	}
	run := func(args ...string) error {
		base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, w.Path, "--path=" + publicDir, "--no-color"}
		return w.Runner.Run(ctx, "/usr/sbin/runuser", append(base, args...)...)
	}
	from, to := "http://"+site.Domain, "https://"+site.Domain
	if err := run("search-replace", from, to, "--all-tables-with-prefix", "--precise", "--skip-columns=guid"); err != nil {
		return fmt.Errorf("update serialized WordPress URLs: %w", err)
	}
	if err := run("option", "update", "home", to); err != nil {
		return fmt.Errorf("update WordPress home URL: %w", err)
	}
	if err := run("option", "update", "siteurl", to); err != nil {
		return fmt.Errorf("update WordPress site URL: %w", err)
	}
	return nil
}

func randomPassword() (string, error) {
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}
