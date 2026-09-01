package platform

import (
	"strings"
	"testing"
)

func TestUbuntu2404IsCertified(t *testing.T) {
	info, err := ParseOSRelease(strings.NewReader("ID=ubuntu\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n"), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if err := info.Certified(); err != nil {
		t.Fatal(err)
	}
}

func TestOtherPlatformsAreRejected(t *testing.T) {
	for _, info := range []Info{
		{ID: "debian", VersionID: "12", Arch: "amd64"},
		{ID: "ubuntu", VersionID: "22.04", Arch: "amd64"},
		{ID: "ubuntu", VersionID: "24.04", Arch: "386"},
	} {
		if err := info.Certified(); err == nil {
			t.Fatalf("accepted unsupported platform %#v", info)
		}
	}
}
