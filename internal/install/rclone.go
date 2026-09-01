package install

import (
	"archive/zip"
	"bytes"
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
	rcloneVersion       = "1.75.0"
	rclonePath          = "/usr/local/lib/wpx/rclone"
	maxRcloneArchive    = 40 << 20
	maxRcloneBinarySize = 80 << 20
)

var rcloneSHA256 = map[string]string{
	"amd64": "aa2804e08f48250e71009c727124b6341cd0288465804a9a09d14663cabafbaa",
	"arm64": "d0ad88ba4c8e285b7c9efa591e0ab643280a91741e13c27f3a9c0957ccfa5203",
}

func installRclone(ctx context.Context) error {
	want, ok := rcloneSHA256[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("rclone is unavailable for architecture %s", runtime.GOARCH)
	}
	archiveURL := fmt.Sprintf("https://downloads.rclone.org/v%s/rclone-v%s-linux-%s.zip", rcloneVersion, rcloneVersion, runtime.GOARCH)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
	if err != nil {
		return fmt.Errorf("download rclone %s: %w", rcloneVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download rclone %s: unexpected HTTP status %s", rcloneVersion, response.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxRcloneArchive+1))
	if err != nil {
		return err
	}
	if len(archive) > maxRcloneArchive {
		return errors.New("rclone download exceeds size limit")
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != want {
		return errors.New("rclone SHA-256 verification failed")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("open rclone archive: %w", err)
	}
	wantedName := fmt.Sprintf("rclone-v%s-linux-%s/rclone", rcloneVersion, runtime.GOARCH)
	var binary *zip.File
	for _, file := range reader.File {
		if file.Name == wantedName {
			binary = file
			break
		}
	}
	if binary == nil || binary.UncompressedSize64 > maxRcloneBinarySize {
		return errors.New("rclone archive does not contain the expected bounded binary")
	}
	input, err := binary.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(rclonePath), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(rclonePath), ".rclone-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	written, err := io.Copy(tmp, io.LimitReader(input, maxRcloneBinarySize+1))
	if err != nil || written > maxRcloneBinarySize {
		tmp.Close()
		return errors.New("extract rclone within size limit")
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
	return os.Rename(name, rclonePath)
}
