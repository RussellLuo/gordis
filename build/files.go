package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func jsonBytes(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	return append(raw, '\n'), err
}

// Only used with our JSON data types, which contain no custom marshalers.
func clone[T any](value T) T {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	var detached T
	if err := json.Unmarshal(raw, &detached); err != nil {
		panic(err)
	}
	return detached
}

func decodeStream[T any](raw []byte) ([]T, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	values := []T{}
	for {
		var value T
		if err := decoder.Decode(&value); err != nil {
			if err == io.EOF {
				return values, nil
			}
			return nil, err
		}
		values = append(values, value)
	}
}

func writeFile(root, name string, raw []byte) error {
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	return os.WriteFile(file, raw, 0644)
}

func fileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func checkFiles(files map[string][]byte, project bool) error {
	// The current filesystem may be case insensitive. Reject aliases before
	// writing, including aliases for library-owned files and directories.
	folded := map[string]string{}
	for _, name := range fileNames(files) {
		key := strings.ToLower(name)
		if old, exists := folded[key]; exists {
			return fmt.Errorf("file name collision: %s and %s", old, name)
		}
		folded[key] = name
	}

	for _, name := range fileNames(files) {
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name {
			return fmt.Errorf("invalid relative file name %q", name)
		}
		top, _, _ := strings.Cut(strings.ToLower(name), "/")
		if top == "project" || top == "logs" || top == "dependencies.json" || (project && (top == "go.mod" || top == "go.sum")) {
			return fmt.Errorf("reserved file name %q", name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := folded[strings.ToLower(parent)]; exists {
				return fmt.Errorf("file/directory conflict: %s and %s", parent, name)
			}
		}
	}
	return nil
}

func projectHashes(root string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("generated file %s is not regular", file)
		}

		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}

		hashes[filepath.ToSlash(relative)] = digest(raw)
		return nil
	})
	return hashes, err
}

// sourceInputs hashes locally mapped compiler inputs and embed data. Every
// local mapping also contributes its module files, even when unused by Go.
func sourceInputs(packages []packageInfo, replacements []ResolvedReplacement) ([]InputRecord, error) {
	sources := map[string]string{}
	inputs := map[string]map[string]string{}
	for _, replacement := range replacements {
		if replacement.Source.Directory != "" {
			sources[replacement.Module] = replacement.Source.Directory
			inputs[replacement.Module] = map[string]string{}
		}
	}

	add := func(module, file string) error {
		absolute, err := filepath.EvalSymlinks(file)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(sources[module], absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("source input %s is outside mapping for %s", file, module)
		}

		raw, err := os.ReadFile(absolute)
		if err != nil {
			return err
		}

		inputs[module][filepath.ToSlash(relative)] = digest(raw)
		return nil
	}
	for _, pkg := range packages {
		if pkg.Module == nil || sources[pkg.Module.Path] == "" {
			continue
		}
		// A version-scoped local mapping may be present but unused after MVS.
		// Only hash package files when Go actually loaded that local source.
		if pkg.Module.Replace == nil || pkg.Module.Replace.Path != sources[pkg.Module.Path] || pkg.Module.Replace.Version != "" {
			continue
		}
		for _, files := range [][]string{
			pkg.GoFiles,
			pkg.CgoFiles,
			pkg.CFiles,
			pkg.CXXFiles,
			pkg.MFiles,
			pkg.HFiles,
			pkg.FFiles,
			pkg.SFiles,
			pkg.SwigFiles,
			pkg.SwigCXXFiles,
			pkg.SysoFiles,
			pkg.EmbedFiles,
		} {
			for _, file := range files {
				if err := add(pkg.Module.Path, filepath.Join(pkg.Dir, file)); err != nil {
					return nil, err
				}
			}
		}
	}

	records := []InputRecord{}
	for module, directory := range sources {
		for _, name := range []string{"go.mod", "go.sum"} {
			file := filepath.Join(directory, name)
			if _, err := os.Stat(file); os.IsNotExist(err) && name == "go.sum" {
				continue
			}
			if err := add(module, file); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(inputs[module])
		records = append(records, InputRecord{Module: module, Directory: directory, Files: inputs[module], SHA256: digest(raw)})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Module < records[j].Module })
	return records, nil
}
