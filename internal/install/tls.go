package install

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func ensurePanelCertificate(certPath, keyPath string, allowCreate bool) error {
	certInfo, certErr := os.Lstat(certPath)
	keyInfo, keyErr := os.Lstat(keyPath)
	for _, err := range []error{certErr, keyErr} {
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect panel TLS files: %w", err)
		}
	}
	if certErr == nil && !certInfo.Mode().IsRegular() || keyErr == nil && !keyInfo.Mode().IsRegular() {
		return errors.New("panel TLS files must be regular files")
	}
	if certErr == nil && keyErr == nil {
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
			return fmt.Errorf("existing panel TLS pair is invalid; restore it before retrying: %w", err)
		}
		return os.Chmod(keyPath, 0600)
	}
	if !allowCreate {
		return errors.New("panel TLS files are missing from an existing installation; restore them before retrying")
	}
	// The first installation can stop between writing the certificate and key.
	// No config was saved yet, so no running panel can depend on that incomplete
	// pair. Once config exists, missing TLS material needs explicit recovery.
	return generateSelfSignedCertificate(certPath, keyPath)
}

func generateSelfSignedCertificate(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate panel TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "WPX local panel"},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create panel certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0750); err != nil {
		return err
	}
	if err := atomicWrite(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return err
	}
	return atomicWrite(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wpx-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(name, path)
}
