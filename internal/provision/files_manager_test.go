//go:build linux

package provision

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func activeFileTestSite() model.Site {
	return model.Site{ID: "files-example", Domain: "files.example.com", Kind: model.Static, Status: "active"}
}

func TestChunkUploadResumesWithoutRewritingAndPublishesExactBytes(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := activeFileTestSite()
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte{0x19}, 350_000)
	second := bytes.Repeat([]byte{0xe7}, 350_000)
	uploadID := "upload_0123456789abcdef"
	result, err := host.UploadFileChunk(context.Background(), site, "large.bin", uploadID, 0, first, false, false)
	if err != nil || result.Next != int64(len(first)) {
		t.Fatalf("first chunk: %+v, %v", result, err)
	}
	result, err = host.UploadFileChunk(context.Background(), site, "large.bin", uploadID, int64(len(first)), second, false, false)
	if err != nil || result.Next != int64(len(first)+len(second)) {
		t.Fatalf("second chunk: %+v, %v", result, err)
	}
	// This is the retry after a response was lost. The backend confirms its
	// durable offset after comparing bytes and does not append the chunk again.
	result, err = host.UploadFileChunk(context.Background(), site, "large.bin", uploadID, 0, first, false, false)
	if err != nil || result.Next != int64(len(first)+len(second)) || result.Complete {
		t.Fatalf("replayed chunk: %+v, %v", result, err)
	}
	result, err = host.UploadFileChunk(context.Background(), site, "large.bin", uploadID, int64(len(first)+len(second)), nil, true, false)
	if err != nil || !result.Complete {
		t.Fatalf("publish: %+v, %v", result, err)
	}
	result, err = host.UploadFileChunk(context.Background(), site, "large.bin", uploadID, int64(len(first)+len(second)), nil, true, false)
	if err != nil || !result.Complete {
		t.Fatalf("replayed publish: %+v, %v", result, err)
	}
	want := append(append([]byte(nil), first...), second...)
	got, err := os.ReadFile(filepath.Join(host.SiteRoot, site.ID, "public", "large.bin"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("uploaded content was corrupted: bytes=%d err=%v", len(got), err)
	}
}

func TestFileManagerRejectsArchiveTraversalAndSelectedSymlinks(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := activeFileTestSite()
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(host.SiteRoot, site.ID, "public")
	archivePath := filepath.Join(public, "bad.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(archive)
	member, _ := zw.Create("../escaped.txt")
	_, _ = member.Write([]byte("bad"))
	_ = zw.Close()
	_ = archive.Close()
	if _, err := host.ExtractFile(context.Background(), site, "bad.zip", "."); err == nil {
		t.Fatal("archive traversal was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(public), "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped public root: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(public, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := host.CopyFiles(context.Background(), site, []string{"link"}, ".", false); err == nil {
		t.Fatal("selected symlink was copied")
	}
}

func TestFileManagerCreatesCopiesMovesAndDeletesDirectories(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := activeFileTestSite()
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := host.CreateDirectory(ctx, site, "assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.UploadFileChunk(ctx, site, "assets/a.txt", "upload_abcdefghijklmnop", 0, []byte("hello"), true, false); err != nil {
		t.Fatal(err)
	}
	if err := host.CreateDirectory(ctx, site, "copies"); err != nil {
		t.Fatal(err)
	}
	if changed, err := host.CopyFiles(ctx, site, []string{"assets"}, "copies", false); err != nil || changed != 1 {
		t.Fatalf("copy: %d, %v", changed, err)
	}
	if changed, err := host.MoveFiles(ctx, site, []string{"copies/assets/a.txt"}, ".", false); err != nil || changed != 1 {
		t.Fatalf("move: %d, %v", changed, err)
	}
	if changed, err := host.DeleteFiles(ctx, site, []string{"copies", "a.txt"}); err != nil || changed != 2 {
		t.Fatalf("delete: %d, %v", changed, err)
	}
}
