package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis/process"
)

func fixture(t *testing.T) (string, bundleManifest) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"backend", "ui"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	binary := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dir, "backend/sampler"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := process.Manifest{
		ID:        "sampler",
		Version:   "1.1.0",
		Entry:     "sampler",
		SHA256:    fmt.Sprintf("%x", sha256.Sum256(binary)),
		Protocol:  process.Protocol,
		Contracts: appplugin.RequiredContracts(),
	}
	writeFixtureJSON(t, filepath.Join(dir, "backend/manifest.json"), manifest)
	js := []byte("export const apiVersion = 2;")
	if err := os.WriteFile(filepath.Join(dir, "ui/index.js"), js, 0644); err != nil {
		t.Fatal(err)
	}
	m := bundleManifest{
		Instances:     []instanceTemplate{{ID: "alpha", Title: "Alpha", Config: json.RawMessage(`{}`)}},
		ID:            "sampler",
		Version:       "1.1.0",
		Backend:       "backend/manifest.json",
		BackendSHA256: manifest.SHA256,
		UI: uiPackage{
			ID:         "sampler",
			Version:    "1.1.0",
			APIVersion: 2,
			Activation: "backend",
			Entry:      "ui/index.js",
			Styles:     []string{},
		},
		Files: map[string]string{
			"ui/index.js": fmt.Sprintf("%x", sha256.Sum256(js)),
		},
	}
	writeFixtureJSON(t, filepath.Join(dir, "bundle.json"), m)
	return dir, m
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRejectsMismatchesBeforeActivation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string, *bundleManifest)
	}{
		{"UI version", func(_ string, m *bundleManifest) { m.UI.Version = "2.1.0" }},
		{"missing application backend", func(_ string, m *bundleManifest) { m.Backend, m.BackendSHA256 = "", "" }},
		{"backend version", func(_ string, m *bundleManifest) { m.Version = "2.1.0"; m.UI.Version = "2.1.0" }},
		{"backend digest", func(_ string, m *bundleManifest) { m.BackendSHA256 = strings.Repeat("0", 64) }},
		{"missing entry", func(_ string, m *bundleManifest) { m.UI.Entry = "ui/missing.js" }},
		{"asset escape", func(_ string, m *bundleManifest) { m.Files["ui/../../secret"] = "bad" }},
		{"digest", func(_ string, m *bundleManifest) { m.Files["ui/index.js"] = strings.Repeat("0", 64) }},
		{"symlink escape", func(dir string, _ *bundleManifest) {
			outside := filepath.Join(t.TempDir(), "outside.js")
			if err := os.WriteFile(outside, []byte("export const apiVersion = 2;"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, "ui/index.js")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, "ui/index.js")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, m := fixture(t)
			test.mutate(dir, &m)
			writeFixtureJSON(t, filepath.Join(dir, "bundle.json"), m)
			if _, err := loadBundle(dir); err == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
}

func TestBrowserAssetsAreSnapshotsAndNeverExposeBackend(t *testing.T) {
	dir, _ := fixture(t)
	b, err := loadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	assets := t.TempDir()
	writeFixtureJSON(t, filepath.Join(assets, "build.json"), map[string]any{"imports": map[string]string{}})
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("__BASE__ __IMPORT_MAP__"), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp("/nested/demo/", assets, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.Shutdown(context.Background())
	a.bundles[b.digest] = b
	h, err := a.handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui/index.js"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", b.view(a.base).Entry, nil))
	if response.Code != 200 ||
		strings.Contains(response.Body.String(), "changed") ||
		!strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
		t.Fatal(response)
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", a.base+"assets/plugins/sampler/"+b.digest+"/backend/sampler", nil))
	if response.Code != 404 {
		t.Fatal(response.Code)
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest("POST", a.base+"api/package/uninstall", nil)
	request.Header.Set("Origin", "https://other.example")
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal(response.Code)
	}
}
