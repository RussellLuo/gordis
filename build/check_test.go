package build

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRequestAndGeneratedFileBoundaries(t *testing.T) {
	for _, name := range []string{
		"dir",
		"module path",
		"query",
		"duplicate package",
		"mixed source",
		"empty source",
		"timeout",
		"validation args",
		"generator",
	} {
		t.Run(name, func(t *testing.T) {
			request, adapter := localRequest(t), valueAdapter()
			switch name {
			case "dir":
				request.Dir = ""
			case "module path":
				request.ModulePath = "../invalid"
			case "query":
				request.Packages[0].Query = Query{}
			case "duplicate package":
				request.Packages = append(request.Packages, request.Packages[0])
			case "mixed source":
				request.Packages[0].Source = &Source{Directory: ".", Module: "example.com/a", Query: "latest"}
			case "empty source":
				request.Packages[0].Source = &Source{}
			case "timeout":
				adapter.Validate.Timeout = -1
			case "validation args":
				adapter.Validate.Args = nil
			case "generator":
				adapter.Generate = nil
			}
			if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "input:") {
				t.Fatal(err)
			}
		})
	}

	// Unsafe and library-owned paths fail before generated files are written.
	for _, invalid := range []string{
		"/absolute.go",
		"../escape.go",
		"a/../escape.go",
		"a//file.go",
		"a\\file.go",
		"C:/file.go",
		"go.mod",
		"GO.MOD",
		"go.sum",
		"logs/overwrite",
		"PROJECT/overwrite",
		"dependencies.json",
		".",
	} {
		t.Run(invalid, func(t *testing.T) {
			request := Request{ModulePath: "example.com/compiled-test", Dir: t.TempDir(), Output: "program"}
			adapter := valueAdapter()
			adapter.Generate = func(Plan) (Project, error) {
				return Project{
					Files: map[string][]byte{"main.go": []byte("package main\nfunc main(){}\n"), invalid: []byte("untrusted")},
				}, nil
			}
			if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "generate:") {
				t.Fatal(err)
			}
			absentOutput(t, request)
		})
	}

	// Valid path spellings can still collide or omit the required root entry.
	for _, files := range []map[string][]byte{
		{"main.go": []byte("package main\nfunc main(){}\n"), "file": {}, "file/nested": {}},
		{"main.go": []byte("package main\nfunc main(){}\n"), "Main.go": []byte("package main\n")},
		{"main.go": []byte("package main\nfunc broken(")},
		{"sub/main.go": []byte("package main\nfunc main(){}\n")},
	} {
		request := Request{ModulePath: "example.com/compiled-test", Dir: t.TempDir(), Output: "program"}
		adapter := valueAdapter()
		adapter.Generate = func(Plan) (Project, error) { return Project{Files: files}, nil }
		if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "generate:") {
			t.Fatal(err)
		}
		absentOutput(t, request)
	}
}

func TestProductionClosureDeclarations(t *testing.T) {
	request := localRequest(t)
	adapter := valueAdapter()
	adapter.Generate = func(Plan) (Project, error) {
		return Project{
			Files: map[string][]byte{"main.go": []byte(`package main
import ("fmt"; "example.com/helper"; _ "example.com/selection/a"; _ "example.com/selection/b")
func main(){ fmt.Println(helper.Value) }
`)},
		}, nil
	}
	if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "without a declared") {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Declaring a module allows direct imports without choosing another factory.
	request.Modules = []ModuleRequirement{{Path: "example.com/helper", Query: Query{Value: "latest"}}}
	if _, err := Build(context.Background(), request, adapter); err != nil {
		t.Fatal(err)
	}

	// Every selected package must remain reachable from the generated entry.
	request.Output = "unselected"
	adapter.Generate = func(Plan) (Project, error) {
		return Project{Files: map[string][]byte{"main.go": []byte("package main\nfunc main(){}\n")}}, nil
	}
	if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatal(err)
	}
	absentOutput(t, request)

	// Command packages cannot be selected for application assembly.
	request.Output = "main-package"
	request.Modules = nil
	request.Packages = []Package{{Path: "example.com/selection/command", Query: Query{Value: "latest"}}}
	if _, err := Build(context.Background(), request, valueAdapter()); err == nil || !strings.Contains(err.Error(), "not an importable package") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}

func TestBuilderDoesNotEnterProductionClosure(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	builder := reflect.TypeFor[Request]().PkgPath()
	request := Request{
		ModulePath: "example.com/compiled-test",
		Dir:        t.TempDir(),
		Output:     "program",
		Packages:   []Package{{Path: builder, Query: Query{Value: "latest"}, Source: &Source{Directory: dir}}},
	}
	adapter := valueAdapter()
	adapter.Generate = func(Plan) (Project, error) {
		return Project{
			Files: map[string][]byte{"main.go": []byte("package main\nimport _ " + `"` + builder + `"` + "\nfunc main(){}\n")},
		}, nil
	}
	if _, err := Build(context.Background(), request, adapter); err == nil || !strings.Contains(err.Error(), "closure contains builder") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}

func TestDefaultRecordUsesEmptyCollections(t *testing.T) {
	request := Request{ModulePath: "example.com/compiled-test", Dir: t.TempDir(), Output: "program"}
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(result.RecordPath, "record.json"))
	if err != nil || strings.Contains(string(raw), "null") || !strings.HasSuffix(string(raw), "\n") {
		t.Fatal(err, string(raw))
	}

	for _, key := range []string{"packages", "requirements", "replacements", "inputs"} {
		if !strings.Contains(string(raw), `"`+key+`": []`) {
			t.Fatal("missing empty collection", key)
		}
	}
}
