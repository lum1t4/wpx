//go:build linux

package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

const wordpressConfigName = "wp-config.php"

func openWordPressDebugSiteRoot(h *Host, site model.Site) (int, error) {
	path := h.SiteRoot + "/" + site.ID
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, fmt.Errorf("open site root safely: %w", err)
	}
	return fd, nil
}

func secureReadWordPressConfig(h *Host, site model.Site) ([]byte, error) {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, wordpressConfigName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, fmt.Errorf("open wp-config.php safely: %w", err)
	}
	return readEditableFD(fd, wordpressConfigName)
}

func secureSetWordPressDebug(ctx context.Context, h *Host, site model.Site, identity Identity, enabled bool, logPath string) error {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, wordpressConfigName, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return fmt.Errorf("open wp-config.php safely: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return errors.New("wp-config.php must be a regular file, not a symbolic link")
	}
	content, err := readEditableFD(fd, wordpressConfigName)
	if err != nil {
		return err
	}
	updated, changed, err := updateWordPressDebug(content, enabled, logPath)
	if err != nil || !changed {
		return err
	}
	temporary, err := randomTemporaryName()
	if err != nil {
		return err
	}
	owner := Identity{UID: int(stat.Uid), GID: int(stat.Gid)}
	if err := createFileAt(rootFD, temporary, updated, os.FileMode(stat.Mode&0777), owner); err != nil {
		return fmt.Errorf("write temporary wp-config.php: %w", err)
	}
	defer unix.Unlinkat(rootFD, temporary, 0)
	temporaryFD, err := unix.Openat2(rootFD, temporary, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return fmt.Errorf("open temporary WordPress configuration safely: %w", err)
	}
	temporaryFile := os.NewFile(uintptr(temporaryFD), temporary)
	if temporaryFile == nil {
		unix.Close(temporaryFD)
		return errors.New("create temporary WordPress configuration handle")
	}
	defer temporaryFile.Close()
	linter, ok := h.Runner.(pinnedPHPFileLinter)
	if !ok {
		return errors.New("provision runner cannot validate a pinned PHP file")
	}
	if err := linter.LintPHPFile(ctx, identity, "/usr/bin/php"+site.PHPVersion, temporaryFile); err != nil {
		return fmt.Errorf("validate WordPress configuration syntax: %w", err)
	}
	var descriptorStat, entryStat unix.Stat_t
	if err := unix.Fstat(temporaryFD, &descriptorStat); err != nil {
		return fmt.Errorf("inspect linted WordPress configuration: %w", err)
	}
	if err := unix.Fstatat(rootFD, temporary, &entryStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || entryStat.Mode&unix.S_IFMT != unix.S_IFREG || descriptorStat.Dev != entryStat.Dev || descriptorStat.Ino != entryStat.Ino {
		return errors.New("temporary WordPress configuration changed during syntax validation")
	}
	if err := unix.Renameat(rootFD, temporary, rootFD, wordpressConfigName); err != nil {
		return fmt.Errorf("replace wp-config.php safely: %w", err)
	}
	// The temporary file was fsynced before rename. A directory fsync improves
	// crash durability, but a failure here must not report rollback: the new
	// configuration is already visible and removing its log would be unsafe.
	_ = unix.Fsync(rootFD)
	return nil
}

func (r ExecRunner) LintPHPFile(ctx context.Context, identity Identity, executable string, file *os.File) error {
	command := exec.CommandContext(ctx, executable, "-l", "-f", "/proc/self/fd/3")
	command.ExtraFiles = []*os.File{file}
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(identity.UID), Gid: uint32(identity.GID)}}
	} else if os.Geteuid() != identity.UID || os.Getegid() != identity.GID {
		return errors.New("cannot lint PHP configuration as the site identity")
	}
	output := cappedDebugLintOutput{limit: (64 << 10) + 1}
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if output.buffer.Len() > 64<<10 {
		return errors.New("PHP syntax validation output exceeded 64 KiB")
	}
	if err != nil {
		return fmt.Errorf("run %s syntax check: %w", filepath.Base(executable), err)
	}
	return nil
}

type cappedDebugLintOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (output *cappedDebugLintOutput) Write(content []byte) (int, error) {
	written := len(content)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(content) > remaining {
			content = content[:remaining]
		}
		_, _ = output.buffer.Write(content)
	}
	return written, nil
}

func openWordPressDebugDirectory(h *Host, site model.Site) (int, error) {
	rootFD, err := openWordPressDebugSiteRoot(h, site)
	if err != nil {
		return -1, err
	}
	fd, err := unix.Openat2(rootFD, "tmp", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	unix.Close(rootFD)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

func securePrepareWordPressDebugLog(h *Host, site model.Site, identity Identity) (bool, error) {
	rootFD, err := openWordPressDebugSiteRoot(h, site)
	if err != nil {
		return false, err
	}
	defer unix.Close(rootFD)
	dirFD, err := unix.Openat2(rootFD, "tmp", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(rootFD, "tmp", 0700); err != nil {
			return false, err
		}
		dirFD, err = unix.Openat2(rootFD, "tmp", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	}
	if err != nil {
		return false, fmt.Errorf("open private log directory safely: %w", err)
	}
	defer unix.Close(dirFD)
	if err := unix.Fchmod(dirFD, 0700); err != nil {
		return false, err
	}
	if err := unix.Fchown(dirFD, identity.UID, identity.GID); err != nil {
		return false, err
	}
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(dirFD, "wordpress-debug.log", flags, 0640)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat2(dirFD, "wordpress-debug.log", &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	}
	if err != nil {
		return false, fmt.Errorf("open private debug log safely: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, errors.New("private debug log must be a regular file")
	}
	if err := unix.Fchmod(fd, 0640); err != nil {
		return false, err
	}
	if err := unix.Fchown(fd, identity.UID, identity.GID); err != nil {
		return false, err
	}
	return created, nil
}

func secureWordPressDebugLogStatus(h *Host, site model.Site) (model.WordPressDebugStatus, error) {
	dirFD, err := openWordPressDebugDirectory(h, site)
	if errors.Is(err, unix.ENOENT) {
		return model.WordPressDebugStatus{}, nil
	}
	if err != nil {
		return model.WordPressDebugStatus{}, fmt.Errorf("open private debug log directory: %w", err)
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat2(dirFD, "wordpress-debug.log", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		return model.WordPressDebugStatus{}, nil
	}
	if err != nil {
		return model.WordPressDebugStatus{}, fmt.Errorf("open private debug log safely: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return model.WordPressDebugStatus{}, errors.New("private debug log must be a regular file")
	}
	return model.WordPressDebugStatus{LogExists: true, LogSize: stat.Size, LogModifiedAt: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC().Format(time.RFC3339)}, nil
}

func secureReadWordPressDebugLog(h *Host, site model.Site, limit int64) (model.WordPressDebugLog, error) {
	dirFD, err := openWordPressDebugDirectory(h, site)
	if errors.Is(err, unix.ENOENT) {
		return model.WordPressDebugLog{}, nil
	}
	if err != nil {
		return model.WordPressDebugLog{}, err
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat2(dirFD, "wordpress-debug.log", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		return model.WordPressDebugLog{}, nil
	}
	if err != nil {
		return model.WordPressDebugLog{}, fmt.Errorf("open private debug log safely: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wordpress-debug.log")
	if file == nil {
		unix.Close(fd)
		return model.WordPressDebugLog{}, errors.New("create private debug log handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return model.WordPressDebugLog{}, errors.New("private debug log must be a regular file")
	}
	start := info.Size() - limit
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return model.WordPressDebugLog{}, err
	}
	content, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return model.WordPressDebugLog{}, err
	}
	if truncated {
		if newline := strings.IndexByte(string(content), '\n'); newline >= 0 {
			content = content[newline+1:]
		}
	}
	return model.WordPressDebugLog{Content: strings.ToValidUTF8(string(content), "�"), Size: info.Size(), Truncated: truncated}, nil
}

func secureClearWordPressDebugLog(h *Host, site model.Site) error {
	dirFD, err := openWordPressDebugDirectory(h, site)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat2(dirFD, "wordpress-debug.log", &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open private debug log safely: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("private debug log must be a regular file")
	}
	if err := unix.Ftruncate(fd, 0); err != nil {
		return fmt.Errorf("clear private debug log: %w", err)
	}
	return unix.Fsync(fd)
}

func secureRemoveWordPressDebugLog(h *Host, site model.Site) error {
	dirFD, err := openWordPressDebugDirectory(h, site)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	err = unix.Unlinkat(dirFD, "wordpress-debug.log", 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
