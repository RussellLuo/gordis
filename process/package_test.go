package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageValidation(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "plugin")
	data := []byte("example artifact")
	digest := sha256.Sum256(data)
	if err := os.WriteFile(entry, data, 0700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{
		ID:        "test.plugin",
		Version:   "1",
		Entry:     "plugin",
		SHA256:    hex.EncodeToString(digest[:]),
		Protocol:  Protocol,
		Contracts: []string{"test/1"},
	}
	path := filepath.Join(dir, "manifest.json")
	write := func() {
		t.Helper()
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := LoadPackage(path, "test/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackage(path, "unknown/1"); err == nil {
		t.Fatal("unknown contract accepted")
	}
	m.SHA256 = "wrong"
	write()
	if _, err := LoadPackage(path); err == nil {
		t.Fatal("digest ignored")
	}
	m.SHA256 = hex.EncodeToString(digest[:])
	m.Entry = "escape"
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	write()
	if _, err := LoadPackage(path); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
