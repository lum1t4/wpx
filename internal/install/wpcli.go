package install

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	wpCLIVersion = "2.12.0"
	wpCLIURL     = "https://github.com/wp-cli/wp-cli/releases/download/v" + wpCLIVersion + "/wp-cli-" + wpCLIVersion + ".phar"
	wpCLISHA512  = "be928f6b8ca1e8dfb9d2f4b75a13aa4aee0896f8a9a0a1c45cd5d2c98605e6172e6d014dda2e27f88c98befc16c040cbb2bd1bfa121510ea5cdf5f6a30fe8832"
	wpCLIPath    = "/usr/local/lib/wpx/wp-cli.phar"
	maxWPCLISize = 16 << 20
)

func installWPCLI(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(wpCLIPath), 0755); err != nil {
		return fmt.Errorf("create WP-CLI directory: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, wpCLIURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download WP-CLI %s: %w", wpCLIVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download WP-CLI %s: unexpected HTTP status %s", wpCLIVersion, response.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(wpCLIPath), ".wp-cli-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	hash := sha512.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(response.Body, maxWPCLISize+1))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("write WP-CLI: %w", err)
	}
	if written > maxWPCLISize {
		tmp.Close()
		return errors.New("WP-CLI download exceeds size limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != wpCLISHA512 {
		tmp.Close()
		return errors.New("WP-CLI SHA-512 verification failed")
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, wpCLIPath); err != nil {
		return fmt.Errorf("install WP-CLI: %w", err)
	}
	return nil
}
