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
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

const maxEditableFile = 1 << 20

func (h *Host) ListFiles(_ context.Context, site model.Site, requested string) ([]broker.FileEntry, error) {
	rootFD, publicDir, err := h.openPublicRoot(site)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, true)
	if err != nil {
		return nil, err
	}
	dirFD := rootFD
	if relative != "." {
		dirFD, err = unix.Openat2(rootFD, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return nil, fmt.Errorf("open directory: %w", err)
		}
		defer unix.Close(dirFD)
	}
	duplicate, err := unix.Dup(dirFD)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(duplicate), publicDir)
	if dir == nil {
		unix.Close(duplicate)
		return nil, errors.New("create directory handle")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]broker.FileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := entry.Name()
		if relative != "." {
			path = filepath.ToSlash(filepath.Join(relative, entry.Name()))
		}
		result = append(result, broker.FileEntry{Name: entry.Name(), Path: path, IsDir: entry.IsDir(), Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (h *Host) ReadFile(_ context.Context, site model.Site, requested string) (string, error) {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return "", err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return "", err
	}
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	content, err := readEditableFD(fd, relative)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(content) {
		return "", errors.New("binary files cannot be edited")
	}
	return string(content), nil
}

func (h *Host) WriteFile(ctx context.Context, site model.Site, requested, content string) error {
	// Authorization can predate a queued domain change or deletion. Share their
	// host lock until the replacement is complete, and open the tree afterwards.
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(content) > maxEditableFile || !utf8.ValidString(content) {
		return errors.New("file must be UTF-8 text no larger than 1 MiB")
	}
	rootFD, publicDir, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return err
	}
	parent, base := filepath.Dir(relative), filepath.Base(relative)
	parentFD := rootFD
	if parent != "." {
		parentFD, err = unix.Openat2(rootFD, parent, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return fmt.Errorf("open parent directory: %w", err)
		}
		defer unix.Close(parentFD)
	}
	oldContent, oldMode, exists, err := readExistingAt(parentFD, base)
	if err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return err
	}
	temporary, err := randomTemporaryName()
	if err != nil {
		return err
	}
	mode := os.FileMode(0640)
	if exists {
		mode = oldMode.Perm()
	}
	if err := createFileAt(parentFD, temporary, []byte(content), mode, identity); err != nil {
		return err
	}
	defer unix.Unlinkat(parentFD, temporary, 0)
	if strings.EqualFold(filepath.Ext(base), ".php") && (site.Kind == model.PHP || site.Kind == model.WordPress) {
		temporaryPath := filepath.Join(publicDir, parent, temporary)
		if err := h.Runner.Run(ctx, "/usr/sbin/runuser", "--user", identity.Name, "--", "/usr/bin/php"+site.PHPVersion, "-l", temporaryPath); err != nil {
			return fmt.Errorf("PHP syntax validation failed: %w", err)
		}
	}
	if exists {
		if err := h.writeRevision(site.ID, relative, oldContent, oldMode); err != nil {
			return err
		}
	}
	if err := unix.Renameat(parentFD, temporary, parentFD, base); err != nil {
		return fmt.Errorf("replace file atomically: %w", err)
	}
	return nil
}

func (h *Host) openPublicRoot(site model.Site) (int, string, error) {
	if err := model.ValidateSite(site); err != nil {
		return -1, "", err
	}
	if err := h.validate(); err != nil {
		return -1, "", err
	}
	publicDir := filepath.Join(h.SiteRoot, site.ID, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return -1, "", err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, publicDir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, "", fmt.Errorf("open site public root safely: %w", err)
	}
	return fd, publicDir, nil
}

func normalizeRelativePath(requested string, allowRoot bool) (string, error) {
	if strings.ContainsRune(requested, 0) || strings.Contains(requested, `\`) || filepath.IsAbs(requested) {
		return "", errors.New("invalid relative path")
	}
	clean := filepath.Clean(requested)
	if clean == "." && allowRoot {
		return clean, nil
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the site public root")
	}
	return clean, nil
}

func readEditableFD(fd int, name string) ([]byte, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("create file handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("editable target is not a regular file")
	}
	if info.Size() > maxEditableFile {
		return nil, errors.New("file exceeds the 1 MiB editor limit")
	}
	return io.ReadAll(io.LimitReader(file, maxEditableFile+1))
}

func readExistingAt(parentFD int, name string) ([]byte, os.FileMode, bool, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("open existing file: %w", err)
	}
	content, err := readEditableFD(fd, name)
	if err != nil {
		return nil, 0, false, err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, 0, false, err
	}
	return content, os.FileMode(stat.Mode), true, nil
}

func randomTemporaryName() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ".wpx-save-" + hex.EncodeToString(b), nil
}

func createFileAt(parentFD int, name string, content []byte, mode os.FileMode, identity Identity) error {
	fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return errors.New("create temporary file handle")
	}
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Chown(identity.UID, identity.GID)
}

func (h *Host) writeRevision(siteID, relative string, content []byte, mode os.FileMode) error {
	if !filepath.IsAbs(h.DataRoot) || h.DataRoot == "/" {
		return errors.New("invalid revision root")
	}
	digest := sha256.Sum256([]byte(relative))
	root := filepath.Join(h.DataRoot, "revisions", siteID, hex.EncodeToString(digest[:16]))
	if err := os.MkdirAll(root, 0700); err != nil {
		return fmt.Errorf("create revision directory: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%03o", time.Now().UTC().UnixNano(), mode.Perm())
	if err := atomicWrite(filepath.Join(root, name), content, 0600); err != nil {
		return fmt.Errorf("write file revision: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err == nil && len(entries) > 20 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries[:len(entries)-20] {
			if !entry.IsDir() {
				_ = os.Remove(filepath.Join(root, entry.Name()))
			}
		}
	}
	return nil
}
