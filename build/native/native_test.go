package native_test

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	builder "github.com/RussellLuo/gordis/build"
	"github.com/RussellLuo/gordis/build/native"
)

func TestCommandPolicyAndDeterministicGeneration(t *testing.T) {
	command := native.Command()
	if command.Name != "gordis-build" || command.DefaultOutput != "gordis-app" || !command.AcceptApp {
		t.Fatal(command)
	}

	// Prepare selects the application separately from optional factories and
	// adds the core requirement needed by the generated entry.
	app := builder.Package{Path: "example.com/application/app", Query: builder.Query{Value: "latest"}}
	factory := builder.Package{Path: "example.com/optional", Query: builder.Query{Value: "v1.2.3", Explicit: true}}
	source := builder.Replacement{Module: "example.com/optional", Source: builder.Source{Directory: "../source"}}
	args := builder.Arguments{
		App: &app,
		Request: builder.Request{
			Dir:          "/caller",
			Output:       "program",
			Packages:     []builder.Package{factory},
			Replacements: []builder.Replacement{source},
		},
	}
	request, adapter, err := command.Prepare(args)
	if err != nil {
		t.Fatal(err)
	}
	if request.ModulePath != "example.com/gordis-compiled" || request.Dir != "/caller" || request.Output != "program" ||
		!reflect.DeepEqual(request.Packages, []builder.Package{factory, app}) || !reflect.DeepEqual(request.Replacements, args.Request.Replacements) ||
		len(request.Modules) != 1 || request.Modules[0].Path != "github.com/RussellLuo/gordis" || request.Modules[0].Query != (builder.Query{Value: "latest"}) ||
		!reflect.DeepEqual(adapter.Validate.Args, []string{"--check"}) || adapter.Validate.Timeout != 0 || adapter.Record != nil {
		t.Fatal(request, adapter)
	}

	// Generation order is stable even if the caller supplies an unsorted plan.
	plan := builder.Plan{
		Packages: []builder.ResolvedPackage{{Path: app.Path}, {Path: "example.com/z"}, {Path: factory.Path}},
	}
	first, err := adapter.Generate(plan)
	if err != nil {
		t.Fatal(err)
	}

	plan.Packages[0], plan.Packages[2] = plan.Packages[2], plan.Packages[0]
	second, err := adapter.Generate(plan)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("generation depends on plan order", err)
	}
	if _, err := adapter.Generate(builder.Plan{}); err == nil {
		t.Fatal("missing app accepted")
	}

	// Native role and positional policies run before source resolution.
	for _, bad := range []builder.Arguments{
		{},
		{App: &app, Positionals: []string{"v1"}},
		{App: &app, Request: builder.Request{Packages: []builder.Package{app}}},
	} {
		if _, _, err := command.Prepare(bad); err == nil {
			t.Fatal("invalid native request accepted", bad)
		}
	}
}

func write(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyModule(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") && entry.Name() != "go.mod" {
			return nil
		}

		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path)
		if err == nil {
			write(t, target, relative, string(data))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func execute(t *testing.T, command *exec.Cmd, wantCode int, contains string) string {
	t.Helper()
	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if code != wantCode || !strings.Contains(string(output), contains) {
		t.Fatalf("%v: exit %d (want %d), output %q (want %q)", command.Args, code, wantCode, output, contains)
	}
	return string(output)
}

// This exercises the real command and independent minimal module. All fixture
// changes happen in a temporary copy, never in the checked-in example or core.
func TestNativeCommandMinimal(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	buildDir := filepath.Dir(filepath.Dir(file))
	coreDir := filepath.Dir(buildDir)
	root := t.TempDir()
	appDir := filepath.Join(root, "source tree")
	copyModule(t, filepath.Join(buildDir, "examples/minimal"), appDir)
	tool := filepath.Join(root, "gordis-build")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	compile := exec.Command("go", "build", "-o", tool, "./cmd/gordis-build")
	compile.Dir = buildDir
	execute(t, compile, 0, "")

	cli := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tool, args...)
		cmd.Dir = root
		return cmd
	}
	app := "example.com/gordis-minimal/app"
	hello := "example.com/gordis-minimal/plugins/hello"
	chinese := "example.com/gordis-minimal/plugins/chinese"
	base := []string{
		"--app",
		app,
		"--replace",
		"example.com/gordis-minimal=" + appDir,
		"--replace",
		"github.com/RussellLuo/gordis=" + coreDir,
	}
	output := func(name string) string {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return filepath.Join(root, name)
	}
	absent := func(path string) {
		t.Helper()
		for _, target := range []string{path, path + ".build"} {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("unexpected delivery: %s (%v)", target, err)
			}
		}
	}

	t.Run("help and native argument errors without Go", func(t *testing.T) {
		help := cli("--app", "example.com/missing/app", "--help")
		help.Env = append(os.Environ(), "PATH=")
		execute(t, help, 0, "--check")
		for _, test := range []struct {
			args []string
			want string
		}{
			{nil, "--app is required"},
			{[]string{"--app"}, "requires a value"},
			{[]string{"--app="}, "requires a nonempty value"},
			{[]string{"--app", app, "--app", app}, "duplicate --app"},
			{[]string{"--app", app, "version"}, "unexpected positional"},
			{[]string{"--app", app, "--", "--help"}, "unexpected positional"},
			{[]string{"--app", app, "--with", app}, "cannot also be selected"},
		} {
			cmd := cli(test.args...)
			cmd.Env = append(os.Environ(), "PATH=")
			execute(t, cmd, 2, test.want)
		}
	})

	checkClosure := func(binary string, selectedChinese bool) {
		t.Helper()
		file, err := os.Open(filepath.Join(binary+".build", "dependencies.json"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		decoder := json.NewDecoder(file)
		foundChinese, foundHello := false, false
		for {
			var pkg struct{ ImportPath string }
			if err := decoder.Decode(&pkg); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(pkg.ImportPath, "github.com/RussellLuo/gordis/build") ||
				strings.Contains(pkg.ImportPath, "github.com/RussellLuo/qi") {
				t.Fatal("builder or Qi entered production closure", pkg.ImportPath)
			}
			foundChinese = foundChinese || pkg.ImportPath == chinese
			foundHello = foundHello || pkg.ImportPath == hello
		}
		if !foundHello || foundChinese != selectedChinese {
			t.Fatal("wrong selected package closure", foundHello, foundChinese)
		}
	}

	defaultBinary := output("gordis-app")
	t.Run("default and optional package", func(t *testing.T) {
		// A plain replacement of the optional package's module must not register it.
		execute(t, cli(base...), 0, "Built gordis-app")
		execute(t, exec.Command(defaultBinary), 0, "Hello, World!\n")
		execute(t, exec.Command(defaultBinary, "--plugin", "greet/chinese"), 1, "greet/chinese")
		if got := execute(t, exec.Command(defaultBinary, "--check"), 0, ""); got != "" {
			t.Fatal("check activated a greeting", got)
		}
		checkClosure(defaultBinary, false)

		// Exercise --app with an explicit fixed query and inline local mapping.
		args := []string{"--app=" + app + "@v0.0.0=" + appDir, "--with=" + chinese,
			"--replace=github.com/RussellLuo/gordis=" + coreDir, "--output=" + output("chinese")}
		execute(t, cli(args...), 0, "Built ")
		execute(t, exec.Command(output("chinese"), "--plugin", "greet/chinese", "--name", "Ada"), 0, "你好，Ada！\n")
		execute(t, exec.Command(output("chinese"), "--name", "Ada"), 0, "Hello, Ada!\n")
		checkClosure(output("chinese"), true)
	})

	t.Run("pure check with activation sentinel", func(t *testing.T) {
		marker := filepath.Join(root, "activated")
		t.Setenv("GORDIS_TEST_ACTIVATION", marker)
		write(t, appDir, "plugins/sentinel/plugin.go", `package sentinel
import ("context"; "os"; "github.com/RussellLuo/gordis")
type Plugin struct { Name string `+"`json:\"name\"`"+` }
func Plugins() []gordis.Plugin { return []gordis.Plugin{new(Plugin)} }
func (*Plugin) Spec() gordis.PluginSpec { return gordis.PluginSpec{ID:"greet/sentinel", New:func() gordis.Plugin{return new(Plugin)}} }
func (*Plugin) Activate(context.Context, *gordis.Scope) error { return os.WriteFile(os.Getenv("GORDIS_TEST_ACTIVATION"), []byte("activated"), 0600) }
`)
		args := append(append([]string{}, base...), "--with", "example.com/gordis-minimal/plugins/sentinel", "--output", output("sentinel"))
		execute(t, cli(args...), 0, "Built ")
		execute(t, exec.Command(output("sentinel"), "--check"), 0, "")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("assembly check activated a plugin", err)
		}
		execute(t, exec.Command(output("sentinel"), "--plugin", "greet/sentinel"), 0, "")
		if raw, err := os.ReadFile(marker); err != nil || string(raw) != "activated" {
			t.Fatal("activation sentinel ineffective", err, string(raw))
		}
	})

	t.Run("contract failures precede delivery", func(t *testing.T) {
		for _, test := range []struct{ name, code, want string }{
			{"missing", "package missing\n", "Plugins"},
			{"wrong", "package wrong\nfunc Plugins() string { return \"wrong\" }\n", "compile:"},
			{
				"arguments",
				"package arguments\nimport \"github.com/RussellLuo/gordis\"\nfunc Plugins(...string) []gordis.Plugin { return nil }\n",
				"compile:",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				write(t, appDir, "plugins/"+test.name+"/plugin.go", test.code)
				binary := output(test.name)
				args := append(append([]string{}, base...), "--with", "example.com/gordis-minimal/plugins/"+test.name, "--output", binary)
				execute(t, cli(args...), 1, test.want)
				absent(binary)
			})
		}

		// Duplicate types are rejected by application validation after compilation.
		duplicate := append(append([]string{}, base...), "--with", hello, "--output", output("duplicate"))
		if got := execute(t, cli(duplicate...), 1, "validate:"); !strings.Contains(got, "greet/hello") {
			t.Fatal("duplicate type diagnostic lost its identity", got)
		}
		absent(output("duplicate"))

		for _, test := range []struct{ name, code, want string }{
			{"wrongrun", "package wrongrun\nfunc Run([]string) error { return nil }\n", "compile:"},
			{"missingrun", "package missingrun\n", "Run"},
			{"mainapp", "package main\nfunc main() {}\n", "main"},
			{
				"checkfailure",
				"package checkfailure\nimport (\"errors\"; \"github.com/RussellLuo/gordis\")\nfunc Run([]string, []gordis.Plugin) error { return errors.New(\"assembly refused\") }\n",
				"assembly refused",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				write(t, appDir, test.name+"/run.go", test.code)
				args := append([]string{}, base...)
				args[1] = "example.com/gordis-minimal/" + test.name
				args = append(args, "--output", output(test.name))
				execute(t, cli(args...), 1, test.want)
				absent(output(test.name))
			})
		}
	})

	t.Run("records inputs and relocated runtime without Go", func(t *testing.T) {
		raw, err := os.ReadFile(defaultBinary + ".build/record.json")
		if err != nil {
			t.Fatal(err)
		}

		var record builder.Record
		if err := json.Unmarshal(raw, &record); err != nil || record.APIVersion != "gordis.build-record/v1alpha1" || !record.LocalDevelopment {
			t.Fatal(err, record)
		}

		foundCheck := false
		for _, cmd := range record.Commands {
			foundCheck = foundCheck || (cmd.Stage == "validate" && reflect.DeepEqual(cmd.Args[1:], []string{"--check"}) && cmd.ExitCode == 0)
		}
		if !foundCheck {
			t.Fatal("missing pure check command", record.Commands)
		}

		original, err := os.ReadFile(filepath.Join(buildDir, "examples/minimal/go.mod"))
		if err != nil {
			t.Fatal(err)
		}

		actual, err := os.ReadFile(filepath.Join(appDir, "go.mod"))
		if err != nil || !bytes.Equal(original, actual) {
			t.Fatal("input go.mod changed", err)
		}
		if _, err := os.Stat(filepath.Join(appDir, "go.sum")); !os.IsNotExist(err) {
			t.Fatal("builder wrote go.sum into input", err)
		}

		movedDir := t.TempDir()
		moved := filepath.Join(movedDir, filepath.Base(defaultBinary))
		if err := os.Rename(defaultBinary, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(appDir, appDir+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(defaultBinary+".build", defaultBinary+".build-moved"); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(moved, "--name", "Moved")
		cmd.Dir = movedDir
		cmd.Env = []string{"PATH="}
		execute(t, cmd, 0, "Hello, Moved!\n")
	})
}
