package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type hardenedHostingRunner struct {
	calls []string
	fail  string
}

func (r *hardenedHostingRunner) Run(_ context.Context, exe string, args ...string) error {
	call := exe + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.fail != "" && strings.Contains(call, r.fail) {
		return errors.New("forced failure")
	}
	return nil
}

type hostingEnv struct{ hardenedHostingRunner }

func (r *hostingEnv) RunEnv(ctx context.Context, _ []string, exe string, args ...string) error {
	return r.Run(ctx, exe, args...)
}
func (r *hostingEnv) OutputEnv(context.Context, []string, string, ...string) ([]byte, error) {
	return nil, nil
}

type hardenedHostingIdentity struct{}

func (hardenedHostingIdentity) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{Name: "wpx-site", UID: 1200, GID: 1300}, nil
}

func TestFTPApplyUsesPanelTLSAndManagedPasswordFile(t *testing.T) {
	root := t.TempDir()
	runner := &hardenedHostingRunner{}
	h := &Host{SiteRoot: filepath.Join(root, "sites"), DataRoot: filepath.Join(root, "data"), ProFTPDConfigRoot: filepath.Join(root, "proftpd"), PanelTLSCertPath: filepath.Join(root, "panel.crt"), PanelTLSKeyPath: filepath.Join(root, "panel.key"), Runner: runner, Identities: hardenedHostingIdentity{}}
	for _, p := range []string{h.PanelTLSCertPath, h.PanelTLSKeyPath} {
		if err := os.WriteFile(p, []byte("tls"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	site := model.Site{ID: "ftp-site", Domain: "ftp.example.com", Kind: model.Static, Status: "active"}
	ftp := model.FTPUser{SiteID: site.ID, Username: "deploy", PasswordHash: "$2y$12$abcdefghijklmnopqrstuv012345678901234567890123456789012", Status: "queued"}
	if err := h.ApplyFTPUser(context.Background(), site, ftp); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(h.ProFTPDConfigRoot, "wpx.conf"))
	auth, _ := os.ReadFile(filepath.Join(h.DataRoot, "proftpd.passwd"))
	if !strings.Contains(string(config), h.PanelTLSCertPath) || !strings.Contains(string(config), "<IfModule !mod_tls.c>") || !strings.HasPrefix(string(auth), ownershipMarker) {
		t.Fatalf("unsafe FTP files config=%s auth=%s", config, auth)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "chown root:root") {
		t.Fatal("password file ownership was not enforced")
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "proftpd-mod-crypto") {
		t.Fatal("TLS module package was not installed")
	}
}

func TestFTPRollbackAndSiteRevoke(t *testing.T) {
	root := t.TempDir()
	runner := &hardenedHostingRunner{fail: "proftpd -t"}
	h := &Host{SiteRoot: filepath.Join(root, "sites"), DataRoot: filepath.Join(root, "data"), ProFTPDConfigRoot: filepath.Join(root, "proftpd"), PanelTLSCertPath: filepath.Join(root, "c"), PanelTLSKeyPath: filepath.Join(root, "k"), Runner: runner, Identities: hardenedHostingIdentity{}}
	_ = os.MkdirAll(h.DataRoot, 0750)
	_ = os.MkdirAll(h.ProFTPDConfigRoot, 0755)
	_ = os.WriteFile(h.PanelTLSCertPath, []byte("c"), 0600)
	_ = os.WriteFile(h.PanelTLSKeyPath, []byte("k"), 0600)
	old := ownershipMarker + "old:hash:1:1::/old:/usr/sbin/nologin\n"
	_ = os.WriteFile(filepath.Join(h.DataRoot, "proftpd.passwd"), []byte(old), 0600)
	site := model.Site{ID: "ftp-site", Domain: "ftp.example.com", Kind: model.Static, Status: "active"}
	ftp := model.FTPUser{SiteID: site.ID, Username: "deploy", PasswordHash: "$2y$12$abcdefghijklmnopqrstuv012345678901234567890123456789012"}
	if err := h.ApplyFTPUser(context.Background(), site, ftp); err == nil {
		t.Fatal("validation failure succeeded")
	}
	got, _ := os.ReadFile(filepath.Join(h.DataRoot, "proftpd.passwd"))
	if string(got) != old {
		t.Fatalf("auth rollback=%q", got)
	}
	stat, err := os.Stat(filepath.Join(h.DataRoot, "proftpd.passwd"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("rolled back password file was not mode 0600: %#v, %v", stat, err)
	}
	runner.fail = ""
	line := "deploy:hash:1:1::" + filepath.Join(h.SiteRoot, site.ID, "public") + ":/usr/sbin/nologin\n"
	_ = os.WriteFile(filepath.Join(h.DataRoot, "proftpd.passwd"), []byte(ownershipMarker+line), 0600)
	if err := h.RemoveSiteFTPAccess(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(h.DataRoot, "proftpd.passwd"))
	if strings.Contains(string(got), "deploy:") {
		t.Fatal("disabled site retained FTP login")
	}
	_ = os.WriteFile(filepath.Join(h.DataRoot, "proftpd.passwd"), []byte(ownershipMarker+line), 0600)
	if err := h.DeleteFTPUser(context.Background(), site, ftp); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteFTPUser(context.Background(), site, ftp); err != nil {
		t.Fatalf("FTP deletion was not retryable: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(h.DataRoot, "proftpd.passwd"))
	if strings.Contains(string(got), "deploy:") {
		t.Fatal("deleted FTP user remained in the password file")
	}
}

func TestPostfixInstallIsStoppedConfiguredAndRestarted(t *testing.T) {
	root := t.TempDir()
	env := &hostingEnv{}
	policy := filepath.Join(root, "policy-rc.d")
	_ = os.WriteFile(policy, []byte("original\n"), 0700)
	h := &Host{Runner: &env.hardenedHostingRunner, Environment: env, PostfixConfigPath: filepath.Join(root, "main.cf"), PolicyRCDPath: policy}
	if err := h.ApplyMailService(context.Background(), model.MailService{Enabled: true, Hostname: "mail.example.com"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(h.PostfixConfigPath)
	restored, _ := os.ReadFile(policy)
	calls := strings.Join(env.calls, "\n")
	if !strings.HasPrefix(string(raw), ownershipMarker) || !strings.Contains(string(raw), "inet_interfaces = loopback-only") || string(restored) != "original\n" {
		t.Fatalf("mail config=%s policy=%q", raw, restored)
	}
	if !strings.Contains(calls, "systemctl restart postfix.service") || strings.Contains(calls, "enable --now") {
		t.Fatalf("service activation=%s", calls)
	}
}

func TestPostfixRefusesUnmanagedConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.cf")
	_ = os.WriteFile(path, []byte("myhostname = user.example\n"), 0644)
	env := &hostingEnv{}
	h := &Host{Runner: &env.hardenedHostingRunner, Environment: env, PostfixConfigPath: path, PolicyRCDPath: filepath.Join(root, "policy")}
	if err := h.ApplyMailService(context.Background(), model.MailService{Enabled: true, Hostname: "mail.example.com"}); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("error=%v", err)
	}
	if len(env.calls) != 0 {
		t.Fatal("apt ran before ownership check")
	}
}

func TestFreshPostfixFailureKeepsSafeRetryableConfiguration(t *testing.T) {
	root := t.TempDir()
	env := &hostingEnv{hardenedHostingRunner: hardenedHostingRunner{fail: "postfix check"}}
	h := &Host{Runner: &env.hardenedHostingRunner, Environment: env, PostfixConfigPath: filepath.Join(root, "main.cf"), PolicyRCDPath: filepath.Join(root, "policy")}
	if err := h.ApplyMailService(context.Background(), model.MailService{Enabled: true, Hostname: "mail.example.com"}); err == nil {
		t.Fatal("validation failure succeeded")
	}
	raw, err := os.ReadFile(h.PostfixConfigPath)
	if err != nil || !strings.HasPrefix(string(raw), ownershipMarker) || !strings.Contains(string(raw), "inet_interfaces = loopback-only") {
		t.Fatalf("unsafe failed-install config=%q error=%v", raw, err)
	}
}

func TestHostingManagedFilesRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "managed")
	if err := os.WriteFile(target, []byte(ownershipMarker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedRegularState(link); err == nil {
		t.Fatal("accepted managed-file symlink")
	}
}
