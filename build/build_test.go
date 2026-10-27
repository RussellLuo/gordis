package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		if err := writeFile(root, name, []byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
}

func localRequest(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	// Quoted paths exercise argv handling; the helper provides both an embed
	// input and an unused file so the record must distinguish actual inputs.
	writeFixture(t, root, map[string]string{
		"source 'tree/go.mod":          "module example.com/selection\n\ngo 1.27\nrequire example.com/helper v0.0.0\n",
		"source 'tree/root.go":         "package selection\nfunc Value() string { return \"root\" }\n",
		"source 'tree/a/value.go":      "package a\nimport \"example.com/helper\"\nfunc Value() string { return helper.Value }\n",
		"source 'tree/b/value.go":      "package b\nfunc Value() string { return \"optional\" }\n",
		"source 'tree/command/main.go": "package main\nfunc main() {}\n",
		"helper/go.mod":                "module example.com/helper\n\ngo 1.27\n",
		"helper/value.go":              "package helper\nimport _ \"embed\"\n//go:embed message.txt\nvar Value string\n",
		"helper/message.txt":           "embedded",
		"helper/unused.txt":            "not a compiler input",
	})
	return Request{
		ModulePath: "example.com/compiled-test",
		Dir:        root,
		Output:     "program",
		Packages: []Package{
			{Path: "example.com/selection/a", Query: Query{Value: "latest"}},
			{Path: "example.com/selection/b", Query: Query{Value: "latest"}},
		},
		Replacements: []Replacement{
			{Module: "example.com/selection", Source: Source{Directory: "source 'tree"}},
			{Module: "example.com/helper", Source: Source{Directory: "./helper"}},
		},
	}
}

// This adapter deliberately uses an arbitrary Value function: Build must not
// know application factory names, prototypes, or registration types.
func valueAdapter() Adapter {
	return Adapter{
		Generate: func(plan Plan) (Project, error) {
			var code strings.Builder
			code.WriteString("package main\nimport (\n\"fmt\"\n\"os\"\n")
			for i, pkg := range plan.Packages {
				fmt.Fprintf(&code, "p%d %q\n", i, pkg.Path)
			}
			code.WriteString(")\nfunc main() { if len(os.Args)>1 && os.Args[1]==\"--inspect\" { return }; fmt.Println(")
			for i := range plan.Packages {
				fmt.Fprintf(&code, "p%d.Value(),", i)
			}
			code.WriteString(") }\n")
			return Project{
				Files: map[string][]byte{"main.go": []byte(code.String()), "asset.txt": []byte("generated resource")},
			}, nil
		},
		Validate: Validation{Args: []string{"--inspect"}},
	}
}

func readRecord(t *testing.T, result Result) Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(result.RecordPath, "record.json"))
	if err != nil {
		t.Fatal(err)
	}

	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.APIVersion != "gordis.build-record/v1alpha1" {
		t.Fatal(record.APIVersion)
	}
	return record
}

func absentOutput(t *testing.T, request Request) {
	t.Helper()
	output := request.Output
	if !filepath.IsAbs(output) {
		output = filepath.Join(request.Dir, output)
	}

	for _, file := range []string{output, output + ".build"} {
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatalf("failed build left %s: %v", file, err)
		}
	}
}

func TestLocalBuildRecordsInputsAndDeterministicProject(t *testing.T) {
	request := localRequest(t)
	writeFixture(t, request.Dir, map[string]string{"unused/go.mod": "module example.com/unused\n\ngo 1.27\n"})
	request.Replacements = append(request.Replacements, Replacement{Module: "example.com/unused", Source: Source{Directory: "unused"}})
	before, err := projectHashes(filepath.Join(request.Dir, "source 'tree"))
	if err != nil {
		t.Fatal(err)
	}

	// Caller environment must not turn the native build into a workspace,
	// cross-compiled or CGO build with hidden build flags.
	t.Setenv("GOWORK", filepath.Join(request.Dir, "absent.work"))
	t.Setenv("GOFLAGS", "-invalid-flag")
	t.Setenv("GOOS", "invalid-os")
	t.Setenv("CGO_ENABLED", "1")
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	// Intent, effective environment, and build evidence are recorded separately.
	record := readRecord(t, result)
	if !record.LocalDevelopment ||
		record.BinarySHA256 != result.BinarySHA256 ||
		!filepath.IsAbs(result.BinaryPath) ||
		len(record.Plan.Requirements) != 1 {
		t.Fatal(record, result)
	}
	if !reflect.DeepEqual(record.Request.Packages, request.Packages) ||
		record.Request.Replacements[0].Source.Directory != "source 'tree" {
		t.Fatal("request intent changed", record.Request)
	}
	if requirement := record.Plan.Requirements[0]; requirement.QueryExecuted || !requirement.DevelopmentPlaceholder || requirement.RequiredVersion != "v0.0.0" {
		t.Fatal(requirement)
	}
	if record.Environment["GOWORK"] != "off" ||
		record.Environment["GOFLAGS"] != "" ||
		record.Environment["CGO_ENABLED"] != "0" {
		t.Fatal(record.Environment)
	}
	if record.GeneratedSHA256["asset.txt"] == "" || record.GeneratedSHA256["go.mod"] == "" {
		t.Fatal(record.GeneratedSHA256)
	}

	for _, command := range record.Commands {
		if command.ExitCode != 0 || command.Stage == "" {
			t.Fatal(command)
		}
		if _, err := os.Stat(filepath.Join(result.RecordPath, command.Log)); err != nil {
			t.Fatal(err)
		}
	}

	// Ordinary dependency mappings contribute used files and embed resources.
	foundEmbed := false
	for _, input := range record.Inputs {
		if input.Module == "example.com/helper" {
			foundEmbed = input.Files["message.txt"] != "" && input.Files["unused.txt"] == "" && input.Files["go.mod"] != ""
		}
	}
	if !foundEmbed {
		t.Fatal("ordinary dependency/embedded inputs missing", record.Inputs)
	}

	foundUnused := false
	for _, replacement := range record.Replacements {
		if replacement.Module == "example.com/unused" {
			foundUnused = !replacement.Used
		}
	}
	if !foundUnused {
		t.Fatal("unused replacement was selected or omitted", record.Replacements)
	}

	dependency, err := os.ReadFile(filepath.Join(result.RecordPath, "dependencies.json"))
	if err != nil || bytes.Contains(dependency, []byte(reflect.TypeFor[Request]().PkgPath())) {
		t.Fatal(err, "builder entered production closure")
	}

	// Reordering selections must preserve the generated project and all inputs.
	projectBefore, err := projectHashes(filepath.Join(result.RecordPath, "project"))
	if err != nil {
		t.Fatal(err)
	}

	request.Packages[0], request.Packages[1] = request.Packages[1], request.Packages[0]
	request.Output = "reordered"
	second, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	projectAfter, err := projectHashes(filepath.Join(second.RecordPath, "project"))
	if err != nil || !reflect.DeepEqual(projectBefore, projectAfter) {
		t.Fatal("selection order changed project", err, projectBefore, projectAfter)
	}

	after, err := projectHashes(filepath.Join(request.Dir, "source 'tree"))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("builder changed input", err)
	}

	// Runtime can move away from the source/record and use an empty PATH.
	for _, source := range []string{"source 'tree", "helper"} {
		if err := os.Rename(filepath.Join(request.Dir, source), filepath.Join(request.Dir, source+"-moved")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(result.RecordPath, result.RecordPath+"-moved"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(result.BinaryPath)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + t.TempDir()}
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "embedded optional\n" {
		t.Fatal(err, string(output))
	}
}

func TestRunBuildsThroughPrepare(t *testing.T) {
	request := localRequest(t)
	args := []string{
		"recipe",
		"--with",
		"example.com/selection/a",
		"--replace",
		"example.com/selection=" + filepath.Join(request.Dir, "source 'tree"),
		"--replace",
		"example.com/helper=" + filepath.Join(request.Dir, "helper"),
		"--output",
		filepath.Join(request.Dir, "cli-output"),
	}
	args = append(args, "--app", "example.com/selection/b")
	calls := 0
	command := Command{
		Name:      "recipe-build",
		AcceptApp: true,
		Prepare: func(a Arguments) (Request, Adapter, error) {
			calls++
			if !reflect.DeepEqual(a.Args, args) || !reflect.DeepEqual(a.Positionals, []string{"recipe"}) || !a.OutputSet {
				t.Fatal(a)
			}

			a.Request.ModulePath = request.ModulePath
			if a.App == nil {
				t.Fatal("missing separately parsed application package")
			}

			a.Request.Packages = append(a.Request.Packages, *a.App)
			return a.Request, valueAdapter(), nil
		},
	}
	var out, diagnostic bytes.Buffer
	if code := Run(context.Background(), args, &out, &diagnostic, command); code != 0 ||
		calls != 1 ||
		diagnostic.Len() != 0 ||
		!strings.Contains(out.String(), "Built ") ||
		!strings.Contains(out.String(), "Build record:") {
		t.Fatal(code, calls, out.String(), diagnostic.String())
	}
}

func TestExistingOutputsArePreserved(t *testing.T) {
	for _, suffix := range []string{"", ".build", "symlink"} {
		t.Run(suffix, func(t *testing.T) {
			request := localRequest(t)
			output := filepath.Join(request.Dir, request.Output)
			file := output + suffix
			if suffix == ".build" {
				if err := os.Mkdir(file, 0755); err != nil {
					t.Fatal(err)
				}
				file = filepath.Join(file, "sentinel")
			}
			if suffix == "symlink" {
				if err := os.Symlink("absent-target", output); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}

			_, err := Build(context.Background(), request, valueAdapter())
			if err == nil || !strings.Contains(err.Error(), "input:") || !strings.Contains(err.Error(), "refusing existing") {
				t.Fatal(err)
			}
			if suffix == "symlink" {
				if target, err := os.Readlink(output); err != nil || target != "absent-target" {
					t.Fatal(target, err)
				}
			} else if raw, err := os.ReadFile(file); err != nil || string(raw) != "preserve" {
				t.Fatal(err, string(raw))
			}
		})
	}
}

func TestCallbackSnapshotsAndFailureProtection(t *testing.T) {
	request := localRequest(t)
	adapter := valueAdapter()
	generate := adapter.Generate

	// Mutating the generation snapshot must not corrupt the engine plan seen
	// by Record; a failing encoder must leave no output pair.
	adapter.Generate = func(plan Plan) (Project, error) {
		project, err := generate(clone(plan))
		plan.Packages[0].Path = "example.com/corrupt"
		plan.Requirements[0].RequiredVersion = "v99.0.0"
		plan.Replacements[0].Source.Directory = "/corrupt"
		return project, err
	}
	sentinel := errors.New("record encoder failed")
	adapter.Record = func(facts Facts) (map[string][]byte, error) {
		if facts.Plan.Packages[0].Path != "example.com/selection/a" || facts.Plan.Requirements[0].RequiredVersion != "v0.0.0" {
			t.Fatal("Generate mutated engine plan", facts.Plan)
		}

		facts.GeneratedSHA256["main.go"] = "corrupt"
		return nil, sentinel
	}
	_, err := Build(context.Background(), request, adapter)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "record:") {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Generation failures use the same no-delivery guarantee.
	adapter.Record = nil
	adapter.Generate = func(Plan) (Project, error) { return Project{}, sentinel }
	_, err = Build(context.Background(), request, adapter)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "generate:") {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Successful custom encoding may mutate every nested snapshot without
	// changing the engine-owned result or the delivered binary hash.
	adapter.Generate = generate
	adapter.Record = func(facts Facts) (map[string][]byte, error) {
		facts.BinarySHA256 = "mutated snapshot"
		facts.Plan.Packages[0].Path = "example.com/mutated"
		facts.Plan.Replacements[0].Source.Directory = "/mutated"
		facts.Request.Packages[0].Path = "example.com/mutated"
		facts.Modules[1].Replace.Dir = "/mutated"
		facts.Inputs[0].Files["go.mod"] = "mutated"
		facts.Commands[0].Args[0] = "mutated"
		facts.Environment["GOWORK"] = "mutated"
		facts.GeneratedSHA256["main.go"] = "mutated"
		return map[string][]byte{
			"record.json":            []byte(`{"apiVersion":"example.build/v1"}`),
			"metadata/selected.json": []byte(`{"selected":true}`),
		}, nil
	}
	result, err := Build(context.Background(), request, adapter)
	if err != nil {
		t.Fatal(err)
	}

	binary, err := os.ReadFile(result.BinaryPath)
	if err != nil || result.BinarySHA256 != digest(binary) {
		t.Fatal("Record mutated engine facts", result, err)
	}
	if raw, err := os.ReadFile(filepath.Join(result.RecordPath, "record.json")); err != nil || string(raw) != `{"apiVersion":"example.build/v1"}` {
		t.Fatal(string(raw), err)
	}
	if _, err := os.Stat(filepath.Join(result.RecordPath, "metadata/selected.json")); err != nil {
		t.Fatal(err)
	}
}

func TestValidationTimeoutAndCancellation(t *testing.T) {
	request := Request{ModulePath: "example.com/compiled-test", Dir: t.TempDir(), Output: "program"}
	adapter := Adapter{
		Generate: func(Plan) (Project, error) {
			return Project{
				Files: map[string][]byte{"main.go": []byte("package main\nimport \"time\"\nfunc main(){ time.Sleep(time.Hour) }\n")},
			}, nil
		},
		Validate: Validation{Args: []string{"--inspect"}, Timeout: 50 * time.Millisecond},
	}
	_, err := Build(context.Background(), request, adapter)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "validate:") {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Cancellation before Build must be observed before any callback runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Build(ctx, request, adapter)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Cancellation inside Generate must be observed before compilation.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	adapter.Generate = func(Plan) (Project, error) {
		cancel()
		return Project{}, nil
	}
	_, err = Build(ctx, request, adapter)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "generate:") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}

func TestInputChangesDuringValidationRejectDelivery(t *testing.T) {
	request := localRequest(t)
	adapter := valueAdapter()
	generate := adapter.Generate
	adapter.Generate = func(plan Plan) (Project, error) {
		project, err := generate(plan)
		// Validation adds a previously absent source file, rather than merely
		// editing a known file. The second Go file discovery must detect it.
		project.Files["main.go"] = []byte(fmt.Sprintf(`package main
import ("os"; _ "example.com/selection/a"; _ "example.com/selection/b")
func main() { if err := os.WriteFile(%q, []byte("package a\nconst Added = 1\n"), 0600); err != nil { panic(err) } }
`, filepath.Join(request.Dir, "source 'tree/a/added.go")))
		return project, err
	}
	_, err := Build(context.Background(), request, adapter)
	if err == nil || !strings.Contains(err.Error(), "local source inputs changed") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}
