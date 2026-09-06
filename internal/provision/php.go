package provision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

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
	SocketReady func(context.Context, string) error
}

func (p *AptPHPRuntime) waitReady(ctx context.Context, socket string) error {
	if p.SocketReady != nil {
		return p.SocketReady(ctx, socket)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	for {
		connection, err := dialer.DialContext(readyCtx, "unix", socket)
		if err == nil {
			connection.Close()
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("PHP site socket did not become ready: %w", readyCtx.Err())
		case <-ticker.C:
		}
	}
}

func (p *AptPHPRuntime) Ensure(ctx context.Context, site model.Site, identity Identity, siteDir string) (string, error) {
	if !model.ValidPHPVersion(site.PHPVersion) {
		return "", errors.New("invalid PHP version")
	}
	if p.Runner == nil || !filepath.IsAbs(p.ConfigRoot) || !filepath.IsAbs(p.RunRoot) || !filepath.IsAbs(p.SnippetRoot) {
		return "", errors.New("invalid PHP runtime configuration")
	}
	if err := p.ensurePackages(ctx, site.PHPVersion); err != nil {
		return "", err
	}
	version := site.PHPVersion
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
	if _, _, err := managedFileStateWithMarker(poolPath, phpOwnershipMarker); err != nil {
		return "", fmt.Errorf("inspect managed PHP pool: %w", err)
	}
	if err := atomicWrite(poolPath, []byte(pool), 0644); err != nil {
		return "", fmt.Errorf("write PHP pool: %w", err)
	}
	if err := p.Runner.Run(ctx, "/usr/sbin/php-fpm"+version, "-t"); err != nil {
		return "", fmt.Errorf("validate PHP %s configuration: %w", version, err)
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

func (p *AptPHPRuntime) ensurePackages(ctx context.Context, version string) error {
	fpmBinary := "/usr/sbin/php-fpm" + version
	if _, err := os.Stat(fpmBinary); os.IsNotExist(err) {
		args := append([]string{"install", "-y", "--no-install-recommends"}, phpPackages(version)...)
		if err := p.Runner.Run(ctx, "/usr/bin/apt-get", args...); err != nil {
			return fmt.Errorf("install PHP %s: %w", version, err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect PHP %s: %w", version, err)
	}
	return nil
}

func phpPackages(version string) []string {
	modules := []string{"fpm", "cli", "bcmath", "curl", "gd", "intl", "mbstring", "mysql", "redis", "xml", "zip"}
	// PHP 8.5 builds OPcache into the runtime. Its distribution no longer
	// provides a separate php8.5-opcache package, so requesting it aborts APT.
	if version != "8.5" {
		modules = append(modules, "opcache")
	}
	packages := make([]string, len(modules))
	for i, module := range modules {
		packages[i] = "php" + version + "-" + module
	}
	return packages
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
