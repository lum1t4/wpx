//go:build linux

package provision

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

const (
	maxManagedFileBytes = int64(1 << 30)
	maxTransferBytes    = int64(256 << 20)
	maxManagedEntries   = 10_000
	maxSearchEntries    = 10_000
	maxPathDepth        = 64
)

type transferBudget struct {
	entries  int
	bytes    int64
	deadline time.Time
}

func newTransferBudget() *transferBudget {
	return &transferBudget{deadline: time.Now().Add(12 * time.Second)}
}

type uploadRecord struct {
	Version  int    `json:"version"`
	SiteID   string `json:"site_id"`
	Path     string `json:"path"`
	UploadID string `json:"upload_id"`
	Next     int64  `json:"next_offset"`
	State    string `json:"state"`
}

func (b *transferBudget) add(size int64) error {
	b.entries++
	b.bytes += size
	if b.entries > maxManagedEntries || b.bytes > maxTransferBytes {
		return errors.New("operation exceeds the 10,000 entry or 256 MiB synchronous limit; choose a narrower selection")
	}
	if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		return errors.New("operation exceeds the 12 second synchronous limit; choose a narrower selection")
	}
	return nil
}

type deadlineReader struct {
	reader   io.Reader
	deadline time.Time
}

func (r deadlineReader) Read(p []byte) (int, error) {
	if time.Now().After(r.deadline) {
		return 0, errors.New("operation exceeds the 12 second synchronous limit; choose a narrower selection")
	}
	return r.reader.Read(p)
}

func (h *Host) SearchFiles(_ context.Context, site model.Site, requested, query string, limit int) (broker.FileSearchResult, error) {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return broker.FileSearchResult{}, err
	}
	defer unix.Close(rootFD)
	start, err := normalizeRelativePath(requested, true)
	if err != nil {
		return broker.FileSearchResult{}, err
	}
	startFD, err := openDirectoryAt(rootFD, start)
	if err != nil {
		return broker.FileSearchResult{}, fmt.Errorf("open search root: %w", err)
	}
	defer unix.Close(startFD)
	needle := strings.ToLower(strings.TrimSpace(query))
	result := broker.FileSearchResult{Entries: make([]broker.FileEntry, 0, limit)}
	scanned := 0
	err = walkDirectoryFD(startFD, start, 0, func(entry broker.FileEntry, _ int) error {
		scanned++
		if scanned > maxSearchEntries {
			result.Truncated = true
			return errStopWalk
		}
		if strings.Contains(strings.ToLower(entry.Name), needle) {
			result.Entries = append(result.Entries, entry)
			if len(result.Entries) == limit {
				result.Truncated = true
				return errStopWalk
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return broker.FileSearchResult{}, err
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	return result, nil
}

func (h *Host) DownloadFile(_ context.Context, site model.Site, requested string, offset int64, limit int) (broker.FileDownloadResult, error) {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return broker.FileDownloadResult{}, err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return broker.FileDownloadResult{}, err
	}
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return broker.FileDownloadResult{}, fmt.Errorf("open download: %w", err)
	}
	file := os.NewFile(uintptr(fd), relative)
	if file == nil {
		unix.Close(fd)
		return broker.FileDownloadResult{}, errors.New("create download handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return broker.FileDownloadResult{}, errors.New("download target is not a regular file")
	}
	if offset > info.Size() {
		return broker.FileDownloadResult{}, errors.New("download offset exceeds file size")
	}
	data := make([]byte, limit)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return broker.FileDownloadResult{}, err
	}
	data = data[:n]
	next := offset + int64(n)
	return broker.FileDownloadResult{Data: data, Offset: offset, Next: next, Size: info.Size(), EOF: next == info.Size()}, nil
}

func (h *Host) UploadFileChunk(ctx context.Context, site model.Site, requested, uploadID string, offset int64, data []byte, final, overwrite bool) (broker.FileUploadChunkResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	parentFD, base, err := openParentAt(rootFD, relative)
	if err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	defer unix.Close(parentFD)
	digest := sha256.Sum256([]byte(uploadID + "\x00" + relative))
	temporary := ".wpx-upload-" + hex.EncodeToString(digest[:16])
	recordPath, record, err := h.openUploadRecord(site.ID, relative, uploadID, digest)
	if err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	if record.State == "complete" {
		if publishedUploadAt(parentFD, base, record.Next) {
			return broker.FileUploadChunkResult{Next: record.Next, Complete: true}, nil
		}
		return broker.FileUploadChunkResult{}, errors.New("completed upload target is missing or changed; use a new upload ID")
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if record.State == "active" {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Openat(parentFD, temporary, flags, 0640)
	if errors.Is(err, unix.ENOENT) && record.State == "publishing" {
		if ok := publishedUploadAt(parentFD, base, record.Next); ok {
			record.State = "complete"
			if err := saveUploadRecord(recordPath, record); err != nil {
				return broker.FileUploadChunkResult{}, err
			}
			return broker.FileUploadChunkResult{Next: record.Next, Complete: true}, nil
		}
	}
	if err != nil {
		return broker.FileUploadChunkResult{}, fmt.Errorf("open upload staging file: %w", err)
	}
	file := os.NewFile(uintptr(fd), temporary)
	if file == nil {
		unix.Close(fd)
		return broker.FileUploadChunkResult{}, errors.New("create upload handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return broker.FileUploadChunkResult{}, errors.New("invalid upload staging file")
	}
	if info.Size() != record.Next {
		// A crash may occur after fsync and before the journal replacement. Accept
		// that one pending chunk only after comparing its bytes.
		if info.Size() == offset+int64(len(data)) && record.Next == offset && sameChunk(file, offset, data) {
			record.Next = info.Size()
			if err := saveUploadRecord(recordPath, record); err != nil {
				return broker.FileUploadChunkResult{}, err
			}
		} else {
			return broker.FileUploadChunkResult{Next: record.Next}, errors.New("upload staging state requires a new upload ID")
		}
	}
	replayedFinal := false
	if offset < record.Next {
		if offset+int64(len(data)) <= record.Next && sameChunk(file, offset, data) {
			if !final || offset+int64(len(data)) != record.Next {
				return broker.FileUploadChunkResult{Next: record.Next}, nil
			}
			replayedFinal = true
		} else {
			return broker.FileUploadChunkResult{Next: record.Next}, fmt.Errorf("upload offset mismatch; resume at %d", record.Next)
		}
	}
	if !replayedFinal && offset != record.Next {
		return broker.FileUploadChunkResult{Next: record.Next}, fmt.Errorf("upload offset mismatch; resume at %d", record.Next)
	}
	next := offset + int64(len(data))
	if replayedFinal {
		next = record.Next
	}
	if next > maxManagedFileBytes {
		return broker.FileUploadChunkResult{}, errors.New("upload exceeds the 1 GiB file limit")
	}
	if !replayedFinal {
		if err := ensureFreeSpace(parentFD, int64(len(data))); err != nil {
			return broker.FileUploadChunkResult{}, err
		}
	}
	if !replayedFinal && len(data) != 0 {
		written, err := file.WriteAt(data, offset)
		if err != nil || written != len(data) {
			if err == nil {
				err = io.ErrShortWrite
			}
			return broker.FileUploadChunkResult{}, fmt.Errorf("append upload chunk: %w", err)
		}
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	if err := file.Chown(identity.UID, identity.GID); err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	if err := file.Sync(); err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	record.Next = next
	if err := saveUploadRecord(recordPath, record); err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	if !final {
		return broker.FileUploadChunkResult{Next: next}, nil
	}
	record.State = "publishing"
	if err := saveUploadRecord(recordPath, record); err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	if overwrite {
		if err := rejectDirectoryOrSymlinkAt(parentFD, base); err != nil && !errors.Is(err, unix.ENOENT) {
			return broker.FileUploadChunkResult{}, err
		}
		if err := unix.Renameat(parentFD, temporary, parentFD, base); err != nil {
			return broker.FileUploadChunkResult{}, fmt.Errorf("publish upload: %w", err)
		}
	} else if err := unix.Renameat2(parentFD, temporary, parentFD, base, unix.RENAME_NOREPLACE); err != nil {
		return broker.FileUploadChunkResult{}, fmt.Errorf("publish upload without overwrite: %w", err)
	}
	record.State = "complete"
	if err := saveUploadRecord(recordPath, record); err != nil {
		return broker.FileUploadChunkResult{}, err
	}
	return broker.FileUploadChunkResult{Next: next, Complete: true}, nil
}

func (h *Host) openUploadRecord(siteID, relative, uploadID string, digest [32]byte) (string, uploadRecord, error) {
	if !filepath.IsAbs(h.DataRoot) || h.DataRoot == "/" {
		return "", uploadRecord{}, errors.New("invalid upload journal root")
	}
	dir := filepath.Join(h.DataRoot, "file-uploads", siteID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", uploadRecord{}, fmt.Errorf("create upload journal directory: %w", err)
	}
	path := filepath.Join(dir, hex.EncodeToString(digest[:16])+".json")
	record := uploadRecord{Version: 1, SiteID: siteID, Path: relative, UploadID: uploadID, State: "active"}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := saveUploadRecord(path, record); err != nil {
			return "", uploadRecord{}, err
		}
		return path, record, nil
	}
	if err != nil || json.Unmarshal(content, &record) != nil || record.Version != 1 || record.SiteID != siteID || record.Path != relative || record.UploadID != uploadID || (record.State != "active" && record.State != "publishing" && record.State != "complete") || record.Next < 0 {
		return "", uploadRecord{}, errors.New("invalid upload journal; use a new upload ID")
	}
	return path, record, nil
}

func saveUploadRecord(path string, record uploadRecord) error {
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, content, 0600); err != nil {
		return fmt.Errorf("save upload journal: %w", err)
	}
	return nil
}

func sameChunk(file *os.File, offset int64, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	actual := make([]byte, len(data))
	n, err := file.ReadAt(actual, offset)
	return n == len(data) && err == nil && string(actual) == string(data)
}

func publishedUploadAt(parentFD int, name string, expected int64) bool {
	var stat unix.Stat_t
	return unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW) == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Size == expected
}

func (h *Host) DeleteFiles(_ context.Context, site model.Site, paths []string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return 0, err
	}
	defer unix.Close(rootFD)
	// Measure every selection before changing the tree. This turns the count,
	// byte, depth, and time ceilings into a precondition for destructive work.
	budget := newTransferBudget()
	for _, requested := range paths {
		relative, err := normalizeRelativePath(requested, false)
		if err != nil {
			return 0, err
		}
		parentFD, base, err := openParentAt(rootFD, relative)
		if err != nil {
			return 0, err
		}
		err = measureEntryAt(parentFD, base, budget, 0)
		unix.Close(parentFD)
		if err != nil {
			return 0, fmt.Errorf("delete preflight for %s: %w", relative, err)
		}
	}
	changed := 0
	for _, requested := range paths {
		relative, err := normalizeRelativePath(requested, false)
		if err != nil {
			return changed, err
		}
		parentFD, base, err := openParentAt(rootFD, relative)
		if err != nil {
			return changed, partialSelectionError("delete", changed, err)
		}
		err = removeEntryAt(parentFD, base, 0)
		unix.Close(parentFD)
		if err != nil {
			return changed, partialSelectionError("delete", changed, fmt.Errorf("delete %s: %w", relative, err))
		}
		changed++
	}
	return changed, nil
}

func (h *Host) RenameFile(_ context.Context, site model.Site, requested, newName string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return err
	}
	parentFD, base, err := openParentAt(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if err := rejectSymlinkAt(parentFD, base); err != nil {
		return err
	}
	if err := unix.Renameat2(parentFD, base, parentFD, newName, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("rename without overwrite: %w", err)
	}
	return nil
}

func (h *Host) CreateDirectory(ctx context.Context, site model.Site, requested string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	relative, err := normalizeRelativePath(requested, false)
	if err != nil {
		return err
	}
	parentFD, base, err := openParentAt(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if err := unix.Mkdirat(parentFD, base, 0750); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		_ = unix.Unlinkat(parentFD, base, unix.AT_REMOVEDIR)
		return err
	}
	if err := unix.Fchownat(parentFD, base, identity.UID, identity.GID, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = unix.Unlinkat(parentFD, base, unix.AT_REMOVEDIR)
		return err
	}
	return nil
}

func (h *Host) CopyFiles(ctx context.Context, site model.Site, paths []string, destination string, overwrite bool) (int, error) {
	return h.transferFiles(ctx, site, paths, destination, overwrite, false)
}

func (h *Host) MoveFiles(ctx context.Context, site model.Site, paths []string, destination string, overwrite bool) (int, error) {
	return h.transferFiles(ctx, site, paths, destination, overwrite, true)
}

func (h *Host) transferFiles(ctx context.Context, site model.Site, paths []string, destination string, overwrite, move bool) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return 0, err
	}
	defer unix.Close(rootFD)
	dest, err := normalizeRelativePath(destination, true)
	if err != nil {
		return 0, err
	}
	destFD, err := openDirectoryAt(rootFD, dest)
	if err != nil {
		return 0, fmt.Errorf("open destination: %w", err)
	}
	defer unix.Close(destFD)
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, requested := range paths {
		relative, err := normalizeRelativePath(requested, false)
		if err != nil {
			return changed, err
		}
		if dest != "." && (dest == relative || strings.HasPrefix(dest, relative+"/")) {
			return changed, errors.New("destination cannot be inside a selected directory")
		}
		srcParent, base, err := openParentAt(rootFD, relative)
		if err != nil {
			return changed, err
		}
		if err := rejectSymlinkAt(srcParent, base); err != nil {
			unix.Close(srcParent)
			return changed, err
		}
		if move {
			if overwrite {
				if err := removeExistingAt(destFD, base); err != nil {
					unix.Close(srcParent)
					return changed, err
				}
			}
			flags := uint(0)
			if !overwrite {
				flags = unix.RENAME_NOREPLACE
			}
			err = unix.Renameat2(srcParent, base, destFD, base, flags)
		} else {
			temporary, randomErr := randomTemporaryName()
			if randomErr != nil {
				unix.Close(srcParent)
				return changed, randomErr
			}
			budget := newTransferBudget()
			err = copyEntryAt(srcParent, base, destFD, temporary, identity, budget, 0)
			if err == nil && overwrite {
				err = removeExistingAt(destFD, base)
			}
			if err == nil {
				err = unix.Renameat2(destFD, temporary, destFD, base, unix.RENAME_NOREPLACE)
			}
			if err != nil {
				_ = removeEntryAt(destFD, temporary, 0)
			}
		}
		unix.Close(srcParent)
		if err != nil {
			return changed, partialSelectionError("transfer", changed, fmt.Errorf("transfer %s: %w", relative, err))
		}
		changed++
	}
	return changed, nil
}

func partialSelectionError(action string, changed int, err error) error {
	if changed == 0 {
		return err
	}
	return fmt.Errorf("%s stopped after %d selections; refresh before retry: %w", action, changed, err)
}

func (h *Host) ArchiveFiles(ctx context.Context, site model.Site, paths []string, destination string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	dest, err := normalizeRelativePath(destination, false)
	if err != nil {
		return err
	}
	for _, selected := range paths {
		if dest == selected || strings.HasPrefix(dest, selected+"/") {
			return errors.New("archive destination cannot be inside the selection")
		}
	}
	parentFD, base, err := openParentAt(rootFD, dest)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	temporary, err := randomTemporaryName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0640)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	if file == nil {
		unix.Close(fd)
		return errors.New("create archive handle")
	}
	zw := zip.NewWriter(file)
	budget := newTransferBudget()
	for _, selected := range paths {
		relative, normalizeErr := normalizeRelativePath(selected, false)
		if normalizeErr != nil {
			err = normalizeErr
			break
		}
		err = addZipEntry(rootFD, relative, relative, zw, budget, 0)
		if err != nil {
			break
		}
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	identity, identityErr := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err == nil {
		err = identityErr
	}
	if err == nil {
		err = file.Chown(identity.UID, identity.GID)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat2(parentFD, temporary, parentFD, base, unix.RENAME_NOREPLACE)
	}
	if err != nil {
		_ = unix.Unlinkat(parentFD, temporary, 0)
		return fmt.Errorf("create zip archive: %w", err)
	}
	return nil
}

func (h *Host) ExtractFile(ctx context.Context, site model.Site, requested, destination string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return 0, err
	}
	defer unix.Close(rootFD)
	archivePath, err := normalizeRelativePath(requested, false)
	if err != nil {
		return 0, err
	}
	dest, err := normalizeRelativePath(destination, true)
	if err != nil {
		return 0, err
	}
	destFD, err := openDirectoryAt(rootFD, dest)
	if err != nil {
		return 0, fmt.Errorf("open extraction destination: %w", err)
	}
	defer unix.Close(destFD)
	archiveFD, err := unix.Openat2(rootFD, archivePath, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return 0, fmt.Errorf("open archive: %w", err)
	}
	archive := os.NewFile(uintptr(archiveFD), archivePath)
	if archive == nil {
		unix.Close(archiveFD)
		return 0, errors.New("create archive handle")
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("archive is not a regular file")
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return 0, err
	}
	lower := strings.ToLower(archivePath)
	if strings.HasSuffix(lower, ".zip") {
		zr, err := zip.NewReader(archive, info.Size())
		if err != nil {
			return 0, fmt.Errorf("read zip archive: %w", err)
		}
		changed, err := extractZip(destFD, zr, identity)
		return extractionResult(changed, err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var reader io.Reader = archive
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		gz, err := gzip.NewReader(archive)
		if err != nil {
			return 0, fmt.Errorf("read gzip archive: %w", err)
		}
		defer gz.Close()
		reader = gz
	} else if !strings.HasSuffix(lower, ".tar") {
		return 0, errors.New("supported archives are .zip, .tar, .tar.gz, and .tgz")
	}
	changed, err := extractTar(destFD, tar.NewReader(reader), identity)
	return extractionResult(changed, err)
}

func extractionResult(changed int, err error) (int, error) {
	if err != nil && changed > 0 {
		return changed, fmt.Errorf("stopped after %d entries; destination contains partial changes, refresh before retry: %w", changed, err)
	}
	return changed, err
}

var errStopWalk = errors.New("stop directory walk")

func openDirectoryAt(rootFD int, relative string) (int, error) {
	if relative == "." {
		return unix.Dup(rootFD)
	}
	return unix.Openat2(rootFD, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
}

func openParentAt(rootFD int, relative string) (int, string, error) {
	parent, base := filepath.Dir(relative), filepath.Base(relative)
	fd, err := openDirectoryAt(rootFD, parent)
	if err != nil {
		return -1, "", fmt.Errorf("open parent directory: %w", err)
	}
	return fd, base, nil
}

func rejectSymlinkAt(parentFD int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errors.New("symbolic links are not supported")
	}
	return nil
}

func rejectDirectoryOrSymlinkAt(parentFD int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	typeBits := stat.Mode & unix.S_IFMT
	if typeBits == unix.S_IFLNK || typeBits == unix.S_IFDIR {
		return errors.New("overwrite target must be a regular file")
	}
	return nil
}

func ensureFreeSpace(fd int, incoming int64) error {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return err
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if incoming > available-(64<<20) {
		return errors.New("insufficient disk space; 64 MiB reserve is required")
	}
	return nil
}

func walkDirectoryFD(dirFD int, prefix string, depth int, visit func(broker.FileEntry, int) error) error {
	if depth > maxPathDepth {
		return errors.New("directory depth exceeds 64")
	}
	duplicate, err := unix.Dup(dirFD)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(duplicate), prefix)
	if dir == nil {
		unix.Close(duplicate)
		return errors.New("create directory handle")
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(dirFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("inspect %s relative to directory: %w", entry.Name(), err)
		}
		typeBits := stat.Mode & unix.S_IFMT
		if typeBits == unix.S_IFLNK {
			continue
		}
		if typeBits != unix.S_IFREG && typeBits != unix.S_IFDIR {
			continue
		}
		path := entry.Name()
		if prefix != "." {
			path = filepath.ToSlash(filepath.Join(prefix, entry.Name()))
		}
		isDir := typeBits == unix.S_IFDIR
		item := broker.FileEntry{Name: entry.Name(), Path: path, IsDir: isDir, Size: stat.Size}
		if err := visit(item, dirFD); err != nil {
			return err
		}
		if isDir {
			child, err := unix.Openat2(dirFD, entry.Name(), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
			if err != nil {
				return err
			}
			err = walkDirectoryFD(child, path, depth+1, visit)
			unix.Close(child)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func removeEntryAt(parentFD int, name string, depth int) error {
	if depth > maxPathDepth {
		return errors.New("directory depth exceeds 64")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return errors.New("refuse to operate on a symbolic link")
	case unix.S_IFDIR:
		child, err := unix.Openat2(parentFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		duplicate, err := unix.Dup(child)
		if err != nil {
			unix.Close(child)
			return err
		}
		dir := os.NewFile(uintptr(duplicate), name)
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err == nil {
			for _, entry := range entries {
				if err = removeEntryAt(child, entry.Name(), depth+1); err != nil {
					break
				}
			}
		}
		unix.Close(child)
		if err != nil {
			return err
		}
		return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
	case unix.S_IFREG:
		return unix.Unlinkat(parentFD, name, 0)
	default:
		return errors.New("refuse to delete a special file")
	}
}

func removeExistingAt(parentFD int, name string) error {
	if err := measureEntryAt(parentFD, name, newTransferBudget(), 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	err := removeEntryAt(parentFD, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func measureEntryAt(parentFD int, name string, budget *transferBudget, depth int) error {
	if depth > maxPathDepth {
		return errors.New("directory depth exceeds 64")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return errors.New("refuse to operate on a symbolic link")
	case unix.S_IFREG:
		return budget.add(stat.Size)
	case unix.S_IFDIR:
		if err := budget.add(0); err != nil {
			return err
		}
		fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		duplicate, err := unix.Dup(fd)
		if err != nil {
			unix.Close(fd)
			return err
		}
		dir := os.NewFile(uintptr(duplicate), name)
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err == nil {
			for _, entry := range entries {
				err = measureEntryAt(fd, entry.Name(), budget, depth+1)
				if err != nil {
					break
				}
			}
		}
		unix.Close(fd)
		return err
	default:
		return errors.New("refuse to operate on a special file")
	}
}

func copyEntryAt(srcParent int, srcName string, dstParent int, dstName string, identity Identity, budget *transferBudget, depth int) error {
	if depth > maxPathDepth {
		return errors.New("directory depth exceeds 64")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(srcParent, srcName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return errors.New("refuse to copy a symbolic link")
	case unix.S_IFREG:
		if err := budget.add(stat.Size); err != nil {
			return err
		}
		src, err := unix.Openat2(srcParent, srcName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		dst, err := unix.Openat(dstParent, dstName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, stat.Mode&0777)
		if err != nil {
			unix.Close(src)
			return err
		}
		srcFile, dstFile := os.NewFile(uintptr(src), srcName), os.NewFile(uintptr(dst), dstName)
		_, copyErr := io.Copy(dstFile, io.LimitReader(deadlineReader{reader: srcFile, deadline: budget.deadline}, stat.Size+1))
		if copyErr == nil {
			copyErr = dstFile.Chown(identity.UID, identity.GID)
		}
		if copyErr == nil {
			copyErr = dstFile.Sync()
		}
		srcFile.Close()
		dstFile.Close()
		return copyErr
	case unix.S_IFDIR:
		if err := budget.add(0); err != nil {
			return err
		}
		if err := unix.Mkdirat(dstParent, dstName, stat.Mode&0777); err != nil {
			return err
		}
		if err := unix.Fchownat(dstParent, dstName, identity.UID, identity.GID, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		srcFD, err := unix.Openat2(srcParent, srcName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		dstFD, err := unix.Openat2(dstParent, dstName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			unix.Close(srcFD)
			return err
		}
		duplicate, err := unix.Dup(srcFD)
		if err != nil {
			unix.Close(srcFD)
			unix.Close(dstFD)
			return err
		}
		dir := os.NewFile(uintptr(duplicate), srcName)
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err == nil {
			for _, entry := range entries {
				err = copyEntryAt(srcFD, entry.Name(), dstFD, entry.Name(), identity, budget, depth+1)
				if err != nil {
					break
				}
			}
		}
		unix.Close(srcFD)
		unix.Close(dstFD)
		return err
	default:
		return errors.New("refuse to copy a special file")
	}
}

func addZipEntry(rootFD int, relative, archiveName string, zw *zip.Writer, budget *transferBudget, depth int) error {
	parentFD, base, err := openParentAt(rootFD, relative)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return errors.New("refuse to archive a symbolic link")
	case unix.S_IFREG:
		if err := budget.add(stat.Size); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(archiveName), Method: zip.Deflate}
		header.SetMode(os.FileMode(stat.Mode))
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		fd, err := unix.Openat2(parentFD, base, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), relative)
		_, err = io.Copy(writer, io.LimitReader(deadlineReader{reader: file, deadline: budget.deadline}, stat.Size+1))
		file.Close()
		return err
	case unix.S_IFDIR:
		if depth > maxPathDepth {
			return errors.New("directory depth exceeds 64")
		}
		if err := budget.add(0); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(archiveName) + "/", Method: zip.Store}
		header.SetMode(os.ModeDir | os.FileMode(stat.Mode&0777))
		if _, err := zw.CreateHeader(header); err != nil {
			return err
		}
		dirFD, err := unix.Openat2(parentFD, base, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return err
		}
		duplicate, err := unix.Dup(dirFD)
		if err != nil {
			unix.Close(dirFD)
			return err
		}
		dir := os.NewFile(uintptr(duplicate), relative)
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err == nil {
			for _, entry := range entries {
				childRelative := filepath.Join(relative, entry.Name())
				childArchive := filepath.Join(archiveName, entry.Name())
				err = addZipEntry(rootFD, childRelative, childArchive, zw, budget, depth+1)
				if err != nil {
					break
				}
			}
		}
		unix.Close(dirFD)
		return err
	default:
		return errors.New("refuse to archive a special file")
	}
}

func safeArchivePath(name string) (string, error) {
	name = strings.TrimSuffix(strings.ReplaceAll(name, `\`, "/"), "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", errors.New("archive contains an invalid path")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != name {
		return "", errors.New("archive path escapes the destination")
	}
	if strings.Count(name, "/") >= maxPathDepth {
		return "", errors.New("archive path exceeds depth limit")
	}
	return clean, nil
}

func ensureArchiveParents(rootFD int, relative string, identity Identity) (int, string, error) {
	parts := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, "", err
	}
	if filepath.Dir(relative) != "." {
		for _, part := range parts {
			if err := unix.Mkdirat(current, part, 0750); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(current)
				return -1, "", err
			}
			if err := rejectSymlinkAt(current, part); err != nil {
				unix.Close(current)
				return -1, "", err
			}
			_ = unix.Fchownat(current, part, identity.UID, identity.GID, unix.AT_SYMLINK_NOFOLLOW)
			next, err := unix.Openat2(current, part, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
			unix.Close(current)
			if err != nil {
				return -1, "", err
			}
			current = next
		}
	}
	return current, filepath.Base(relative), nil
}

func writeArchiveFile(rootFD int, relative string, mode os.FileMode, size int64, reader io.Reader, identity Identity, budget *transferBudget) error {
	if size < 0 || size > maxManagedFileBytes {
		return errors.New("archive member exceeds the 1 GiB file limit")
	}
	if err := budget.add(size); err != nil {
		return err
	}
	parentFD, base, err := ensureArchiveParents(rootFD, relative, identity)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()&0770))
	if err != nil {
		return fmt.Errorf("create extracted file: %w", err)
	}
	file := os.NewFile(uintptr(fd), base)
	written, copyErr := io.Copy(file, io.LimitReader(deadlineReader{reader: reader, deadline: budget.deadline}, size+1))
	if copyErr == nil && written != size {
		copyErr = errors.New("archive member size mismatch")
	}
	if copyErr == nil {
		copyErr = file.Chown(identity.UID, identity.GID)
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = unix.Unlinkat(parentFD, base, 0)
	}
	return copyErr
}

func extractZip(destFD int, archive *zip.Reader, identity Identity) (int, error) {
	budget := newTransferBudget()
	for _, member := range archive.File {
		relative, err := safeArchivePath(member.Name)
		if err != nil {
			return budget.entries, err
		}
		mode := member.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return budget.entries, errors.New("archive links and special files are not supported")
		}
		if mode.IsDir() {
			parent, base, err := ensureArchiveParents(destFD, relative, identity)
			if err != nil {
				return budget.entries, err
			}
			err = unix.Mkdirat(parent, base, uint32(mode.Perm()&0770))
			if errors.Is(err, unix.EEXIST) {
				err = rejectSymlinkAt(parent, base)
			}
			if err == nil {
				err = unix.Fchownat(parent, base, identity.UID, identity.GID, unix.AT_SYMLINK_NOFOLLOW)
			}
			unix.Close(parent)
			if err != nil {
				return budget.entries, err
			}
			if err := budget.add(0); err != nil {
				return budget.entries, err
			}
			continue
		}
		reader, err := member.Open()
		if err != nil {
			return budget.entries, err
		}
		err = writeArchiveFile(destFD, relative, mode, int64(member.UncompressedSize64), reader, identity, budget)
		reader.Close()
		if err != nil {
			return budget.entries, err
		}
	}
	return budget.entries, nil
}

func extractTar(destFD int, archive *tar.Reader, identity Identity) (int, error) {
	budget := newTransferBudget()
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return budget.entries, nil
		}
		if err != nil {
			return budget.entries, err
		}
		relative, err := safeArchivePath(header.Name)
		if err != nil {
			return budget.entries, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			parent, base, err := ensureArchiveParents(destFD, relative, identity)
			if err != nil {
				return budget.entries, err
			}
			err = unix.Mkdirat(parent, base, uint32(os.FileMode(header.Mode).Perm()&0770))
			if errors.Is(err, unix.EEXIST) {
				err = rejectSymlinkAt(parent, base)
			}
			if err == nil {
				err = unix.Fchownat(parent, base, identity.UID, identity.GID, unix.AT_SYMLINK_NOFOLLOW)
			}
			unix.Close(parent)
			if err != nil {
				return budget.entries, err
			}
			if err := budget.add(0); err != nil {
				return budget.entries, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeArchiveFile(destFD, relative, os.FileMode(header.Mode), header.Size, archive, identity, budget); err != nil {
				return budget.entries, err
			}
		default:
			return budget.entries, errors.New("archive links and special files are not supported")
		}
	}
}
