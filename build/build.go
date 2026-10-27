package build

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Build resolves request without editing its inputs, generates an isolated Go
// project, compiles and validates it, then exclusively delivers the binary and
// its .build directory. Adapter callbacks receive detached data snapshots.
func Build(ctx context.Context, request Request, adapter Adapter) (result Result, err error) {
	stage := "input"
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", stage, err)
		}
	}()

	if err = ctx.Err(); err != nil {
		return Result{}, err
	}

	request = normalizedCollections(clone(request))
	original := clone(request)
	request, err = checkRequest(request, adapter)
	if err != nil {
		return Result{}, err
	}

	// Keep validation arguments independent from caller/callback slice changes.
	adapter.Validate.Args = append([]string{}, adapter.Validate.Args...)
	work, err := os.MkdirTemp("", "gordis-build-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)

	// Capture the toolchain under the same isolated environment used by all
	// resolution and build commands, before invoking application callbacks.
	r := &runner{ctx: ctx, dir: work, env: nativeEnvironment(), commands: []CommandRecord{}}
	raw, err := r.run(stage, work,
		"go", "env", "-json",
		"GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOWORK", "GOFLAGS",
		"GOTOOLCHAIN", "GOEXPERIMENT", "GOAMD64", "GOARM64",
	)
	if err != nil {
		return Result{}, err
	}

	environment := map[string]string{}
	if err = json.Unmarshal(raw, &environment); err != nil {
		return Result{}, err
	}

	// Record the effective empty flag set, without the whitespace used to
	// suppress persistent GOFLAGS while preserving other Go configuration.
	environment["GOFLAGS"] = strings.TrimSpace(environment["GOFLAGS"])
	goVersion := strings.TrimPrefix(environment["GOVERSION"], "go")
	if !validToolchain(goVersion) {
		return Result{}, fmt.Errorf("unsupported Go toolchain %s", environment["GOVERSION"])
	}

	stage = "resolve"
	plan, err := resolve(r, request, goVersion)
	if err != nil {
		return Result{}, err
	}

	stage = "generate"
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}

	generated, err := adapter.Generate(clone(plan))
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}

	project := filepath.Join(work, "project")
	if err = writeProject(project, plan, generated); err != nil {
		return Result{}, err
	}

	// Generated imports determine the final graph; the resolution plan alone
	// cannot establish selected versions or the production package closure.
	stage = "dependencies"
	for _, args := range [][]string{
		{"go", "mod", "tidy"},
		{"go", "mod", "download", "all"},
		{"go", "mod", "verify"},
	} {
		if _, err = r.run(stage, project, args...); err != nil {
			return Result{}, err
		}
	}

	raw, err = r.run(stage, project, "go", "list", "-mod=readonly", "-m", "-json", "all")
	if err != nil {
		return Result{}, err
	}

	modules, err := decodeStream[ModuleRecord](raw)
	if err != nil {
		return Result{}, err
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	replacements, err := checkModules(modules, plan)
	if err != nil {
		return Result{}, err
	}

	dependencyRaw, packages, err := packageClosure(r, stage, project, plan)
	if err != nil {
		return Result{}, err
	}

	inputs, err := sourceInputs(packages, plan.Replacements)
	if err != nil {
		return Result{}, err
	}

	stage = "compile"
	candidate := platformOutput(filepath.Join(work, "candidate"))
	if _, err = r.run(stage, project, "go", "build", "-mod=readonly", "-trimpath", "-o", candidate, "."); err != nil {
		return Result{}, err
	}

	stage = "validate"
	timeout := adapter.Validate.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	validation, cancel := context.WithTimeout(ctx, timeout)
	_, err = r.runContext(validation, stage, work, append([]string{candidate}, adapter.Validate.Args...)...)
	cancel()
	if err != nil {
		return Result{}, fmt.Errorf("candidate application validation: %w", err)
	}

	stage = "record"
	checkInputs := func() error {
		// Repeat Go's file discovery as well as hashing, so added/deleted Go or
		// embed files cannot silently change the input set during compilation.
		_, currentPackages, err := packageClosure(r, stage, project, plan)
		if err != nil {
			return err
		}

		current, err := sourceInputs(currentPackages, plan.Replacements)
		if err != nil {
			return err
		}
		if !sameJSON(inputs, current) {
			return fmt.Errorf("local source inputs changed during build; retry with a fixed snapshot")
		}
		return nil
	}
	if err = checkInputs(); err != nil {
		return Result{}, err
	}

	generatedHashes, err := projectHashes(project)
	if err != nil {
		return Result{}, err
	}

	binary, err := os.ReadFile(candidate)
	if err != nil {
		return Result{}, err
	}

	// Freeze the command history before encoding. No later command may be
	// omitted from the facts supplied to the application record encoder.
	facts := Facts{
		Request:          original,
		Plan:             plan,
		Modules:          modules,
		Replacements:     replacements,
		Inputs:           inputs,
		Commands:         append([]CommandRecord{}, r.commands...),
		Environment:      environment,
		GeneratedSHA256:  generatedHashes,
		BinarySHA256:     digest(binary),
		LocalDevelopment: len(inputs) > 0,
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}

	var records map[string][]byte
	if adapter.Record != nil {
		records, err = adapter.Record(clone(facts))
	} else {
		raw, err = jsonBytes(Record{APIVersion: "gordis.build-record/v1alpha1", Facts: facts})
		records = map[string][]byte{"record.json": raw}
	}
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if err = checkRecord(records); err != nil {
		return Result{}, err
	}

	// Callbacks are trusted pure computations. Rehash known inputs after the
	// encoder too, without executing any command absent from Facts.Commands.
	finalInputs, err := sourceInputs(packages, plan.Replacements)
	if err != nil || !sameJSON(inputs, finalInputs) {
		if err == nil {
			err = fmt.Errorf("local source inputs changed during record encoding")
		}
		return Result{}, err
	}

	// Assemble the complete record tree privately before reserving any output.
	delivery := filepath.Join(work, "delivery")
	for _, name := range fileNames(records) {
		if err = writeFile(delivery, name, records[name]); err != nil {
			return Result{}, err
		}
	}
	if err = writeFile(delivery, "dependencies.json", dependencyRaw); err != nil {
		return Result{}, err
	}

	for _, name := range []string{"project", "logs"} {
		if err = os.Rename(filepath.Join(work, name), filepath.Join(delivery, name)); err != nil {
			return Result{}, err
		}
	}

	stage = "publish"
	if err = publish(ctx, delivery, candidate, request.Output, request.Output+".build"); err != nil {
		return Result{}, err
	}
	return Result{
		BinaryPath:   request.Output,
		RecordPath:   request.Output + ".build",
		BinarySHA256: facts.BinarySHA256,
	}, nil
}

func packageClosure(r *runner, stage, project string, plan Plan) ([]byte, []packageInfo, error) {
	raw, err := r.run(stage, project, "go", "list", "-mod=readonly", "-deps", "-json", ".")
	if err != nil {
		return nil, nil, err
	}

	packages, err := decodeStream[packageInfo](raw)
	if err == nil {
		err = checkPackages(packages, plan)
	}
	return raw, packages, err
}

func writeProject(dir string, plan Plan, project Project) error {
	if err := checkFiles(project.Files, true); err != nil {
		return err
	}

	entry := false
	for _, name := range fileNames(project.Files) {
		raw := append([]byte{}, project.Files[name]...)
		if strings.HasSuffix(name, ".go") {
			var err error
			raw, err = format.Source(raw)
			if err != nil {
				return fmt.Errorf("generated %s: %w", name, err)
			}
			if path.Dir(name) == "." && !strings.HasSuffix(name, "_test.go") {
				file, err := parser.ParseFile(token.NewFileSet(), name, raw, 0)
				if err != nil {
					return err
				}
				if file.Name.Name == "main" {
					for _, declaration := range file.Decls {
						if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil && function.Name.Name == "main" {
							entry = true
						}
					}
				}
			}
		}
		if err := writeFile(dir, name, raw); err != nil {
			return err
		}
	}
	if !entry {
		return fmt.Errorf("Generate must provide a root Go main entry")
	}

	raw, err := moduleFile(plan)
	if err != nil {
		return err
	}
	return writeFile(dir, "go.mod", raw)
}

func checkRecord(files map[string][]byte) error {
	if err := checkFiles(files, false); err != nil {
		return err
	}

	var object map[string]json.RawMessage
	raw, ok := files["record.json"]
	if !ok || json.Unmarshal(raw, &object) != nil || object == nil {
		return fmt.Errorf("Record must return a JSON object in record.json")
	}
	return nil
}
