package install

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	resticVersion           = "0.19.1"
	resticPath              = "/usr/local/lib/wpx/restic"
	maxResticCompressedSize = 32 << 20
	maxResticBinarySize     = 64 << 20
)

var resticSHA256 = map[string]string{
	"amd64": "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c",
	"arm64": "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465",
}

func installRestic(ctx context.Context) error {
	want, ok := resticSHA256[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("restic is unavailable for architecture %s", runtime.GOARCH)
	}
	url := fmt.Sprintf("https://github.com/restic/restic/releases/download/v%s/restic_%s_linux_%s.bz2", resticVersion, resticVersion, runtime.GOARCH)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
	if err != nil {
		return fmt.Errorf("download restic %s: %w", resticVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download restic %s: unexpected HTTP status %s", resticVersion, response.Status)
	}
	compressed, err := io.ReadAll(io.LimitReader(response.Body, maxResticCompressedSize+1))
	if err != nil {
		return err
	}
	if len(compressed) > maxResticCompressedSize {
		return errors.New("restic download exceeds size limit")
	}
	digest := sha256.Sum256(compressed)
	if hex.EncodeToString(digest[:]) != want {
		return errors.New("restic SHA-256 verification failed")
	}
	if err := os.MkdirAll(filepath.Dir(resticPath), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(resticPath), ".restic-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	written, err := io.Copy(tmp, io.LimitReader(bzip2.NewReader(bytes.NewReader(compressed)), maxResticBinarySize+1))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("decompress restic: %w", err)
	}
	if written > maxResticBinarySize {
		tmp.Close()
		return errors.New("restic binary exceeds size limit")
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
	return os.Rename(name, resticPath)
}
