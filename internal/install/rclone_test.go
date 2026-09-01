package install

import (
	"archive/zip"
	"bytes"
	"fmt"
	"testing"
)

func TestPinnedRcloneAMD64BinaryFitsBound(t *testing.T) {
	const pinnedAMD64Size = 85_323_938
	if pinnedAMD64Size > maxRcloneBinarySize {
		t.Fatalf("pinned rclone amd64 binary is %d bytes; bound is %d", pinnedAMD64Size, maxRcloneBinarySize)
	}
}

func TestFindRcloneBinaryRequiresExpectedArchitecturePath(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create(fmt.Sprintf("rclone-v%s-linux-amd64/rclone", rcloneVersion))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := findRcloneBinary(archive.Bytes(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := findRcloneBinary(archive.Bytes(), "arm64"); err == nil {
		t.Fatal("archive for another architecture was accepted")
	}
}
