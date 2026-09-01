package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wpx.json")
	cfg := Default()
	cfg.DataRoot = filepath.Join(dir, "data")
	cfg.SiteRoot = filepath.Join(dir, "sites")
	cfg.RunRoot = filepath.Join(dir, "run")
	cfg.StatePath = filepath.Join(dir, "data", "state.db")
	cfg.BrokerSocket = filepath.Join(dir, "run", "broker.sock")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("round trip changed config: got %#v want %#v", got, cfg)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("config mode is %o, want 600", st.Mode().Perm())
	}
}

func TestValidateRejectsUnsafeRoots(t *testing.T) {
	cfg := Default()
	cfg.DataRoot = "/"
	if err := cfg.Validate(); err == nil {
		t.Fatal("filesystem root was accepted as data root")
	}
}
