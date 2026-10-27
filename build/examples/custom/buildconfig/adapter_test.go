package buildconfig_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"example.com/gordis-build-custom/buildconfig"
	builder "github.com/RussellLuo/gordis/build"
)

// Package local source as synthetic third-party versions. The production recipe
// must consume normal module dependencies, without any core/build replacement.
// These fixtures are source evidence, not evidence that a version was published.
func proxyModule(t *testing.T, proxy, source, module string) {
	t.Helper()
	const version = "v0.0.0-fixture"
	escaped := module
	for c := 'A'; c <= 'Z'; c++ {
		escaped = strings.ReplaceAll(escaped, string(c), "!"+strings.ToLower(string(c)))
	}

	base := escaped + "/@v/" + version
	mod, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, proxy, base+".mod", string(mod))
	write(t, proxy, base+".info", `{"Version":"`+version+`","Time":"2026-10-27T00:00:00Z"}`)
	file, err := os.Create(filepath.Join(proxy, filepath.FromSlash(base+".zip")))
	if err != nil {
		t.Fatal(err)
	}

	archive := zip.NewWriter(file)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != source {
				if strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && entry.Name() != "go.mod" && entry.Name() != "go.sum" {
			return nil
		}

		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		out, err := archive.Create(module + "@" + version + "/" + filepath.ToSlash(relative))
		if err == nil {
			_, err = out.Write(raw)
		}
		return err
	})
	closeArchive, closeFile := archive.Close(), file.Close()
	if err != nil || closeArchive != nil || closeFile != nil {
		t.Fatal(err, closeArchive, closeFile)
	}
}

func write(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func execute(t *testing.T, cmd *exec.Cmd, wantCode int, want string) string {
	t.Helper()
	raw, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if code != wantCode || !strings.Contains(string(raw), want) {
		t.Fatalf("%v: exit %d, want %d; output %q, want %q", cmd.Args, code, wantCode, raw, want)
	}
	return string(raw)
}

// Run the actual build recipe with copied application source. The source
// mutations below test application contracts without editing the real example.
func TestCustomRecipe(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	appSource := filepath.Dir(filepath.Dir(file))
	buildDir := filepath.Dir(filepath.Dir(appSource))
	coreDir := filepath.Dir(buildDir)
	root := t.TempDir()
	appDir := filepath.Join(root, "application source")
	err := filepath.WalkDir(appSource, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") && entry.Name() != "go.mod" {
			return nil
		}

		relative, err := filepath.Rel(appSource, path)
		if err != nil {
			return err
		}

		raw, err := os.ReadFile(path)
		if err == nil {
			write(t, appDir, relative, string(raw))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	tool := filepath.Join(root, "custom-build")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}

	compile := exec.Command("go", "build", "-o", tool, "./cmd/build")
	compile.Dir = appSource
	execute(t, compile, 0, "")
	// Test normal third-party resolution against a private file proxy.
	proxy := t.TempDir()
	proxyModule(t, proxy, coreDir, "github.com/RussellLuo/gordis")
	proxyModule(t, proxy, buildDir, "github.com/RussellLuo/gordis/build")
	cache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}

	proxyURL := func(dir string) string { return (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir)}).String() }

	// Use a fresh cache; existing dependency archives supply x/mod through a
	// file-only fallback. Synthetic module versions never enter the user's cache.
	t.Setenv("GOPROXY", proxyURL(proxy)+","+proxyURL(filepath.Join(strings.TrimSpace(string(cache)), "cache/download")))
	moduleCache := t.TempDir()
	t.Setenv("GOMODCACHE", moduleCache)

	// Go makes extracted module directories read-only, so restore directory
	// permissions before testing's TempDir cleanup removes this private cache.
	t.Cleanup(func() {
		if err := filepath.WalkDir(moduleCache, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				err = os.Chmod(path, 0755)
			}
			return err
		}); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("GOSUMDB", "off")
	write(t, appDir, "go.mod", `module example.com/gordis-build-custom
go 1.27
require (
    github.com/RussellLuo/gordis v0.0.0-fixture
    github.com/RussellLuo/gordis/build v0.0.0-fixture
)
`)
	output := filepath.Join(root, "greeting")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}

	recipe := func(target string) *exec.Cmd {
		cmd := exec.Command(tool, "--output", target)
		cmd.Dir = appDir
		return cmd
	}

	// Instrument actual activation. A successful static check must leave no file.
	helloPath := filepath.Join(appDir, "plugins/hello/registration.go")
	original, err := os.ReadFile(helloPath)
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(root, "activated")
	modified := strings.Replace(string(original), `"fmt"`, "\"fmt\"\n\"os\"", 1)
	modified = strings.Replace(modified, "fmt.Printf(", "if err := os.WriteFile("+strconv.Quote(marker)+", []byte(p.Name), 0600); err != nil { return err }; fmt.Printf(", 1)
	write(t, appDir, "plugins/hello/registration.go", modified)
	execute(t, recipe(output), 0, "Built ")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("build activated a plugin", err)
	}

	run := func(args ...string) *exec.Cmd {
		cmd := exec.Command(output, args...)
		cmd.Dir = root
		return cmd
	}
	if got := execute(t, run("--verify-assembly"), 0, ""); got != "" {
		t.Fatal("check produced business output", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("explicit check activated a plugin", err)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "Hello, World!\n"},
		{[]string{"--plugin", "greet/chinese"}, "你好，朋友！\n"},
		{[]string{"--name", "Ada"}, "Hello, Ada!\n"},
		{[]string{"--plugin", "greet/chinese", "--name", "Ada"}, "你好，Ada！\n"},
	} {
		if got := execute(t, run(tc.args...), 0, tc.want); got != tc.want {
			t.Fatal(got)
		}
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "Ada" {
		t.Fatal("configured instance did not activate", string(raw), err)
	}

	// Records must separate the build-recipe module graph from the runtime
	// package closure and retain the actual application validation command.
	raw, err := os.ReadFile(output + ".build/record.json")
	if err != nil {
		t.Fatal(err)
	}

	var record builder.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.APIVersion != "gordis.build-record/v1alpha1" || !record.LocalDevelopment {
		t.Fatal(record)
	}

	foundCheck := false
	for _, command := range record.Commands {
		if command.Stage == "validate" && len(command.Args) == 2 && command.Args[1] == "--verify-assembly" && command.ExitCode == 0 {
			foundCheck = true
		}
	}
	if !foundCheck || len(record.Replacements) != 1 || record.Replacements[0].Module != "example.com/gordis-build-custom" {
		t.Fatal("missing validation or unexpected third-party replacement", record)
	}

	// The application's go.mod also serves its build recipe, so the builder
	// remains in the module graph. That is distinct from compiled packages.
	builderInGraph := false
	for _, module := range record.Modules {
		if module.Path == "github.com/RussellLuo/gordis/build" {
			builderInGraph = true
		}
		if strings.HasPrefix(module.Path, "github.com/RussellLuo/gordis") && (module.Version != "v0.0.0-fixture" || module.Replace != nil) {
			t.Fatal("third-party dependency was not resolved normally", module)
		}
	}
	if !builderInGraph {
		t.Fatal("expected build-recipe requirement in module graph")
	}
	if len(record.Inputs) != 1 || record.Inputs[0].Module != "example.com/gordis-build-custom" {
		t.Fatal("third-party modules recorded as local source", record.Inputs)
	}

	raw, err = os.ReadFile(output + ".build/dependencies.json")
	if err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	seen := map[string]bool{}
	for {
		var pkg struct{ ImportPath string }
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if strings.Contains(pkg.ImportPath, "gordis/build") ||
			strings.Contains(pkg.ImportPath, "/buildconfig") ||
			strings.Contains(pkg.ImportPath, "/qi/") {
			t.Fatal("build tooling entered production package closure", pkg)
		}
		seen[pkg.ImportPath] = true
	}

	for _, path := range []string{"app", "plugins/hello", "plugins/chinese"} {
		if !seen["example.com/gordis-build-custom/"+path] {
			t.Fatal("runtime package missing", path)
		}
	}

	// Changing only the application's description must change runtime Config.
	chinese, err := os.ReadFile(filepath.Join(appDir, "plugins/chinese/registration.go"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, appDir, "plugins/chinese/registration.go", strings.Replace(string(chinese), `DefaultName: "朋友"`, `DefaultName: "同学"`, 1))
	changed := output + "-changed"
	execute(t, recipe(changed), 0, "Built ")
	execute(t, exec.Command(changed, "--plugin", "greet/chinese"), 0, "你好，同学！\n")
	write(t, appDir, "plugins/chinese/registration.go", string(chinese))
	for _, tc := range []struct{ name, source, want string }{
		{
			"empty-default",
			strings.Replace(string(chinese), `DefaultName: "朋友"`, `DefaultName: ""`, 1),
			"nonempty default name",
		},
		{
			"duplicate-id",
			strings.Replace(string(chinese), `"greet/chinese"`, `"greet/hello"`, 1),
			"validate:",
		},
		{
			"wrong-factory",
			strings.Replace(string(chinese), "Registration() app.Registration", "Registration(_ int) app.Registration", 1),
			"compile:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, appDir, "plugins/chinese/registration.go", tc.source)
			target := output + "-" + tc.name
			execute(t, recipe(target), 1, tc.want)
			for _, path := range []string{target, target + ".build"} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed assembly left output", path, err)
				}
			}
		})
	}

	// The delivered program runs after both source and record are moved, with no Go.
	for _, path := range []string{appDir, output + ".build"} {
		if err := os.Rename(path, path+"-moved"); err != nil {
			t.Fatal(err)
		}
	}

	moved := filepath.Join(t.TempDir(), filepath.Base(output))
	if err := os.Rename(output, moved); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(moved, "--plugin", "greet/chinese")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + t.TempDir()}
	execute(t, cmd, 0, "你好，朋友！\n")
}

func TestCustomGenerationOrder(t *testing.T) {
	request, adapter := buildconfig.Recipe(".", "greeting")
	if adapter.Record != nil {
		t.Fatal("example must use public default records")
	}

	plan := builder.Plan{}
	for _, pkg := range request.Packages {
		plan.Packages = append(plan.Packages, builder.ResolvedPackage{Path: pkg.Path})
	}

	first, err := adapter.Generate(plan)
	if err != nil {
		t.Fatal(err)
	}

	plan.Packages[0], plan.Packages[2] = plan.Packages[2], plan.Packages[0]
	second, err := adapter.Generate(plan)
	if err != nil || !bytes.Equal(first.Files["main.go"], second.Files["main.go"]) {
		t.Fatal("package order changed entry", err)
	}
}
