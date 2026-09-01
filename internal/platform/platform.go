// Package platform isolates distribution-specific knowledge. Core product code
// must not branch on distribution names; a platform adapter supplies package,
// service, and path behavior for a certified operating system.
package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

type Info struct {
	ID        string
	VersionID string
	Name      string
	Arch      string
}

func Detect() (Info, error) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return Info{}, fmt.Errorf("open /etc/os-release: %w", err)
	}
	defer f.Close()
	return ParseOSRelease(f, runtime.GOARCH)
}

func ParseOSRelease(reader io.Reader, arch string) (Info, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return Info{}, err
	}
	info := Info{ID: values["ID"], VersionID: values["VERSION_ID"], Name: values["PRETTY_NAME"], Arch: arch}
	if info.ID == "" || info.VersionID == "" {
		return Info{}, errors.New("os-release does not identify the platform")
	}
	return info, nil
}

func (i Info) Certified() error {
	if i.ID != "ubuntu" || i.VersionID != "24.04" {
		return fmt.Errorf("WPX currently requires Ubuntu 24.04; detected %s %s", i.ID, i.VersionID)
	}
	if i.Arch != "amd64" && i.Arch != "arm64" {
		return fmt.Errorf("WPX requires amd64 or arm64; detected %s", i.Arch)
	}
	return nil
}
