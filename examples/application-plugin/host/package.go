package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis/process"
)

type uiPackage struct {
	ID         string   `json:"id"`
	Version    string   `json:"version"`
	Title      string   `json:"title"`
	APIVersion int      `json:"apiVersion"`
	Activation string   `json:"activation"`
	Entry      string   `json:"entry"`
	Styles     []string `json:"styles"`
}
type instanceTemplate struct {
	ID     string          `json:"id"`
	Title  string          `json:"title"`
	Config json.RawMessage `json:"config"`
}
type bundleManifest struct {
	Instances     []instanceTemplate `json:"instances"`
	ID            string             `json:"id"`
	Version       string             `json:"version"`
	Backend       string             `json:"backend"`
	BackendSHA256 string             `json:"backendSHA256"`
	UI            uiPackage          `json:"ui"`
	Files         map[string]string  `json:"files"`
}
type bundle struct {
	manifest bundleManifest
	backend  process.Package
	digest   string
	files    map[string][]byte
}

// Only explicitly listed browser assets are served. Snapshot their bytes at
// validation, so later filesystem edits cannot change an immutable asset URL.
func loadBundle(directory string) (*bundle, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "bundle.json"))
	if err != nil {
		return nil, err
	}
	if len(raw) > 64*1024 {
		return nil, fmt.Errorf("bundle manifest too large")
	}
	b := &bundle{files: map[string][]byte{}}
	if err := json.Unmarshal(raw, &b.manifest); err != nil {
		return nil, err
	}
	m := b.manifest
	if !packageName.MatchString(m.ID) ||
		!versionName.MatchString(m.Version) ||
		m.UI.ID != m.ID ||
		m.UI.Version != m.Version ||
		m.UI.APIVersion != 2 ||
		m.UI.Activation != "backend" {
		return nil, fmt.Errorf("incompatible bundle identity or UI API")
	}
	if len(m.Instances) == 0 {
		return nil, fmt.Errorf("no package instances")
	}
	seen := map[string]bool{}
	for _, instance := range m.Instances {
		if !packageName.MatchString(instance.ID) ||
			seen[instance.ID] ||
			instance.Title == "" ||
			!json.Valid(instance.Config) {
			return nil, fmt.Errorf("invalid or duplicate instance template")
		}
		seen[instance.ID] = true
	}
	if m.Backend == "" {
		return nil, fmt.Errorf("external backend required")
	}
	backendPath, err := containedFile(directory, m.Backend)
	if err != nil {
		return nil, err
	}
	b.backend, err = process.LoadPackage(backendPath, appplugin.RequiredContracts()...)
	if err != nil {
		return nil, err
	}
	if b.backend.Manifest.ID != m.ID || b.backend.Manifest.Version != m.Version {
		return nil, fmt.Errorf("backend and UI versions differ")
	}
	if b.backend.Manifest.SHA256 != m.BackendSHA256 ||
		!sameContracts(b.backend.Manifest.Contracts, appplugin.RequiredContracts()) {
		return nil, fmt.Errorf("backend digest or contract mismatch")
	}
	total := 0
	for file, want := range m.Files {
		if !strings.HasPrefix(file, "ui/") {
			return nil, fmt.Errorf("not a browser asset: %s", file)
		}
		full, err := containedFile(directory, file)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		if info.Size() > 8*1024*1024 {
			return nil, fmt.Errorf("UI asset too large")
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > 16*1024*1024 {
			return nil, fmt.Errorf("UI bundle too large")
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != want {
			return nil, fmt.Errorf("UI digest mismatch: %s", file)
		}
		b.files[file] = data
	}
	for _, file := range append([]string{m.UI.Entry}, m.UI.Styles...) {
		if _, ok := b.files[file]; !ok {
			return nil, fmt.Errorf("unlisted UI asset: %s", file)
		}
	}
	hash := sha256.Sum256(raw)
	b.digest = hex.EncodeToString(hash[:])
	return b, nil
}

func sameContracts(actual, required []string) bool {
	if len(actual) != len(required) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range required {
		seen[name] = true
	}
	for _, name := range actual {
		if !seen[name] {
			return false
		}
		delete(seen, name)
	}
	return len(seen) == 0
}

func containedFile(directory, name string) (string, error) {
	if !fs.ValidPath(name) || strings.ContainsAny(name, "?#\\") {
		return "", fmt.Errorf("invalid bundle path: %q", name)
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("asset escapes bundle")
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", name)
	}
	return full, nil
}

func (b *bundle) view(base string) uiPackage {
	p := b.manifest.UI
	prefix := base + "assets/plugins/" + p.ID + "/" + b.digest + "/"
	p.Entry = prefix + p.Entry
	p.Styles = append([]string{}, p.Styles...)
	for i := range p.Styles {
		p.Styles[i] = prefix + p.Styles[i]
	}
	return p
}
