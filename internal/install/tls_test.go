package install

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSelfSignedCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "panel.crt")
	keyPath := filepath.Join(dir, "panel.key")
	if err := generateSelfSignedCertificate(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Fatal(err)
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("key mode is %o, want 600", keyInfo.Mode().Perm())
	}
}

func TestResumePreservesTLSAndRecoversOnlyUnconfiguredPartialPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "panel.crt"), filepath.Join(dir, "panel.key")
	if err := ensurePanelCertificate(certPath, keyPath, false); err == nil {
		t.Fatal("existing installation with lost TLS files must require recovery")
	}
	if err := ensurePanelCertificate(certPath, keyPath, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePanelCertificate(certPath, keyPath, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("TLS key changed on resume: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := ensurePanelCertificate(certPath, keyPath, false); err == nil {
		t.Fatal("existing config must prevent replacing a partial TLS pair")
	}
	if err := ensurePanelCertificate(certPath, keyPath, true); err != nil {
		t.Fatalf("first-run interruption should recover a partial TLS pair: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePanelCertificate(certPath, keyPath, true); err == nil {
		t.Fatal("invalid existing TLS pair must not be overwritten")
	}
}
