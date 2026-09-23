package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Manifest describes an immutable, explicitly supplied local package. It is not
// an installer or signature trust policy. User configuration belongs elsewhere.
type Manifest struct {
	ID        string   `json:"id"`
	Version   string   `json:"version"`
	Entry     string   `json:"entry"`
	SHA256    string   `json:"sha256"`
	Protocol  string   `json:"protocol"`
	Contracts []string `json:"contracts"`
}

type Package struct {
	Manifest   Manifest
	Executable string
}

func LoadPackage(path string, requiredContracts ...string) (Package, error) {
	var pkg Package
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return pkg, err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return pkg, err
	}
	f, err := os.Open(real)
	if err != nil {
		return pkg, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64*1024+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pkg.Manifest); err != nil {
		return pkg, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pkg, errors.New("process: trailing manifest data")
	}
	m := pkg.Manifest
	if m.ID == "" || m.Version == "" || m.Protocol != Protocol || m.Entry == "" || filepath.IsAbs(m.Entry) {
		return pkg, errors.New("process: invalid manifest")
	}
	actual := Identity{m.Protocol, m.ID, m.Version, m.Contracts}
	expected := Identity{Protocol, m.ID, m.Version, requiredContracts}
	if err := compatible(actual, expected); err != nil {
		return pkg, err
	}
	dir := filepath.Dir(real)
	entry, err := filepath.EvalSymlinks(filepath.Join(dir, m.Entry))
	if err != nil {
		return pkg, err
	}
	rel, err := filepath.Rel(dir, entry)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return pkg, errors.New("process: executable escapes package directory")
	}
	binary, err := os.Open(entry)
	if err != nil {
		return pkg, err
	}
	defer binary.Close()
	info, err := binary.Stat()
	if err != nil {
		return pkg, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return pkg, errors.New("process: entry is not executable")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, binary); err != nil {
		return pkg, err
	}
	if len(m.SHA256) != 64 || hex.EncodeToString(hash.Sum(nil)) != m.SHA256 {
		return pkg, fmt.Errorf("process: package %s/%s digest mismatch", m.ID, m.Version)
	}
	pkg.Executable = entry
	return pkg, nil
}

func (p Package) Identity() Identity {
	m := p.Manifest
	return Identity{m.Protocol, m.ID, m.Version, append([]string(nil), m.Contracts...)}
}
