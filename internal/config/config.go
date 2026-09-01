// Package config owns the small amount of process configuration that cannot be
// stored in SQLite. Keeping this file deliberately boring makes recovery possible
// with ordinary Unix tools when the panel database is unavailable.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Config struct {
	ListenAddress      string `json:"listen_address"`
	StatePath          string `json:"state_path"`
	BrokerSocket       string `json:"broker_socket"`
	DataRoot           string `json:"data_root"`
	SiteRoot           string `json:"site_root"`
	RunRoot            string `json:"run_root"`
	TLSCertPath        string `json:"tls_cert_path"`
	TLSKeyPath         string `json:"tls_key_path"`
	SecretKeyPath      string `json:"secret_key_path"`
	BootstrapTokenHash string `json:"bootstrap_token_hash,omitempty"`
	WebUID             uint32 `json:"web_uid"`
	UpdateChecks       bool   `json:"update_checks"`
	UpdateCheckURL     string `json:"update_check_url"`
}

func Default() Config {
	return Config{
		ListenAddress:  "127.0.0.1:9443",
		StatePath:      "/var/lib/wpx/state.db",
		BrokerSocket:   "/run/wpx/broker.sock",
		DataRoot:       "/var/lib/wpx",
		SiteRoot:       "/var/www/wpx",
		RunRoot:        "/run/wpx",
		TLSCertPath:    "/var/lib/wpx/tls/panel.crt",
		TLSKeyPath:     "/var/lib/wpx/tls/panel.key",
		SecretKeyPath:  "/var/lib/wpx/secret.key",
		UpdateChecks:   true,
		UpdateCheckURL: "https://api.github.com/repos/lum1t4/wpx/releases/latest",
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddress) == "" {
		return errors.New("listen_address is empty")
	}
	for name, value := range map[string]string{
		"state_path": c.StatePath, "broker_socket": c.BrokerSocket,
		"data_root": c.DataRoot, "run_root": c.RunRoot,
		"site_root":     c.SiteRoot,
		"tls_cert_path": c.TLSCertPath, "tls_key_path": c.TLSKeyPath,
		"secret_key_path": c.SecretKeyPath,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
		if filepath.Clean(value) != value {
			return fmt.Errorf("%s must already be clean", name)
		}
	}
	if c.DataRoot == "/" || c.SiteRoot == "/" || c.RunRoot == "/" {
		return errors.New("data_root, site_root, and run_root must not be filesystem root")
	}
	if c.UpdateChecks {
		endpoint, err := url.Parse(c.UpdateCheckURL)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
			return errors.New("update_check_url must be an HTTPS URL without credentials or fragment")
		}
	}
	return nil
}

// Save writes a new configuration atomically. A crash can leave the old file or
// the new file, but never a partially truncated JSON document.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	var previous os.FileInfo
	if info, err := os.Stat(path); err == nil {
		previous = info
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect existing config: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if previous != nil {
		if err := os.Chmod(path, previous.Mode().Perm()); err != nil {
			return fmt.Errorf("restore config permissions: %w", err)
		}
		if stat, ok := previous.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
				return fmt.Errorf("restore config ownership: %w", err)
			}
		}
	}
	return nil
}
