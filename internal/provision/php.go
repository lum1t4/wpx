package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/model"
)

// AptPHPRuntime installs one co-installable PHP branch on first use, gives each
// site its own FPM pool and socket, and uses ondemand workers so idle sites do not
// retain PHP workers in memory.
type AptPHPRuntime struct {
	Runner      Runner
	ConfigRoot  string
	RunRoot     string
	SnippetRoot string
}

func (p *AptPHPRuntime) Ensure(ctx context.Context, site model.Site, identity Identity, siteDir string) (string, error) {
	if !model.ValidPHPVersion(site.PHPVersion) {
		return "", errors.New("invalid PHP version")
	}
	if p.Runner == nil || !filepath.IsAbs(p.ConfigRoot) || !filepath.IsAbs(p.RunRoot) || !filepath.IsAbs(p.SnippetRoot) {
		return "", errors.New("invalid PHP runtime configuration")
	}
	version := site.PHPVersion
	fpmBinary := "/usr/sbin/php-fpm" + version
	if _, err := os.Stat(fpmBinary); os.IsNotExist(err) {
		prefix := "php" + version + "-"
		packages := []string{
			prefix + "fpm", prefix + "cli", prefix + "bcmath", prefix + "curl",
			prefix + "gd", prefix + "intl", prefix + "mbstring", prefix + "mysql",
			prefix + "opcache", prefix + "redis", prefix + "xml", prefix + "zip",
		}
		args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
		if err := p.Runner.Run(ctx, "/usr/bin/apt-get", args...); err != nil {
			return "", fmt.Errorf("install PHP %s: %w", version, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect PHP %s: %w", version, err)
	}

	poolDir := filepath.Join(p.ConfigRoot, version, "fpm", "pool.d")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		return "", fmt.Errorf("prepare PHP pool directory: %w", err)
	}
	socket := filepath.Join(p.RunRoot, "wpx-"+site.ID+".sock")
	poolPath := filepath.Join(poolDir, "wpx-"+site.ID+".conf")
	snippetPath, err := ensureManagedSnippetFile(p.SnippetRoot, site.ID, phpOwnershipMarker)
	if err != nil {
		return "", err
	}
	pool := renderPool(site, identity, siteDir, socket, snippetPath)
	if previous, exists, err := managedFileStateWithMarker(poolPath, phpOwnershipMarker); err != nil {
		return "", fmt.Errorf("inspect managed PHP pool: %w", err)
	} else {
		_ = previous
		_ = exists
	}
	if err := atomicWrite(poolPath, []byte(pool), 0644); err != nil {
		return "", fmt.Errorf("write PHP pool: %w", err)
	}
	service := "php" + version + "-fpm.service"
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", service); err != nil {
		return "", fmt.Errorf("start PHP %s: %w", version, err)
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "reload", service); err != nil {
		return "", fmt.Errorf("reload PHP %s: %w", version, err)
	}
	return socket, nil
}

func renderPool(site model.Site, identity Identity, siteDir, socket, snippetPath string) string {
	return phpOwnershipMarker +
		"[wpx-" + site.ID + "]\n" +
		"user = " + identity.Name + "\n" +
		"group = " + identity.Name + "\n" +
		"listen = " + socket + "\n" +
		"listen.owner = " + identity.Name + "\n" +
		"listen.group = www-data\n" +
		"listen.mode = 0660\n" +
		"pm = ondemand\n" +
		"pm.max_children = 5\n" +
		"pm.process_idle_timeout = 10s\n" +
		"pm.max_requests = 500\n" +
		"chdir = " + siteDir + "\n" +
		"clear_env = yes\n" +
		"catch_workers_output = yes\n" +
		"php_admin_value[upload_tmp_dir] = " + filepath.Join(siteDir, "tmp") + "\n" +
		"php_admin_value[session.save_path] = " + filepath.Join(siteDir, "tmp") + "\n" +
		"include = " + snippetPath + "\n"
}
