package build

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/mod/module"
)

func checkRequest(request Request, adapter Adapter) (Request, error) {
	if adapter.Generate == nil || len(adapter.Validate.Args) == 0 || adapter.Validate.Timeout < 0 {
		return request, fmt.Errorf("Generate and nonempty Validate.Args are required; validation timeout must be nonnegative")
	}
	if err := module.CheckPath(request.ModulePath); err != nil {
		return request, fmt.Errorf("generated module: %w", err)
	}
	if request.Dir == "" || request.Output == "" {
		return request, fmt.Errorf("Dir and Output are required")
	}

	dir, err := filepath.Abs(request.Dir)
	if err != nil {
		return request, err
	}

	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return request, err
	}

	info, err := os.Stat(dir)
	if err != nil {
		return request, err
	}
	if !info.IsDir() {
		return request, fmt.Errorf("Dir %s is not a directory", dir)
	}

	request.Dir = dir
	if !filepath.IsAbs(request.Output) {
		request.Output = filepath.Join(dir, request.Output)
	}

	request.Output = filepath.Clean(request.Output)
	for _, target := range []string{request.Output, request.Output + ".build"} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return request, fmt.Errorf("refusing existing or inaccessible output %s", target)
		}
	}

	seen := map[string]bool{}
	for _, pkg := range request.Packages {
		if err := checkPackage(pkg.Path); err != nil {
			return request, err
		}
		if err := checkQuery(pkg.Query); err != nil {
			return request, fmt.Errorf("package %s: %w", pkg.Path, err)
		}
		if seen[pkg.Path] {
			return request, fmt.Errorf("duplicate selected package %s", pkg.Path)
		}
		seen[pkg.Path] = true
		if pkg.Source != nil {
			if err := checkSource(*pkg.Source); err != nil {
				return request, fmt.Errorf("package %s source: %w", pkg.Path, err)
			}
		}
	}

	for _, requirement := range request.Modules {
		if err := module.CheckPath(requirement.Path); err != nil {
			return request, err
		}
		if err := checkQuery(requirement.Query); err != nil {
			return request, fmt.Errorf("module %s: %w", requirement.Path, err)
		}
	}

	for _, replacement := range request.Replacements {
		if err := module.CheckPath(replacement.Module); err != nil {
			return request, err
		}
		if replacement.Version != "" {
			if !fixedVersion(replacement.Version) {
				return request, fmt.Errorf("replacement %s left version must be fixed", replacement.Module)
			}
			if err := module.Check(replacement.Module, replacement.Version); err != nil {
				return request, err
			}
		}
		if err := checkSource(replacement.Source); err != nil {
			return request, fmt.Errorf("replacement %s: %w", replacement.Module, err)
		}
	}
	return request, nil
}

type packageInfo struct {
	ImportPath string
	Name       string
	Dir        string
	Standard   bool
	Module     *ModuleRecord
	Imports    []string

	// Keep Go's input categories separate so local hashes follow the files
	// actually selected for this build, including native code and embed data.
	GoFiles      []string
	CgoFiles     []string
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	SysoFiles    []string
	EmbedFiles   []string
}

func checkModules(modules []ModuleRecord, plan Plan) ([]ReplacementRecord, error) {
	graph := map[string]ModuleRecord{}
	declared := map[string]ResolvedReplacement{}
	for _, replacement := range plan.Replacements {
		declared[replacement.Module] = replacement
	}

	for _, mod := range modules {
		graph[mod.Path] = mod
		if mod.Replace != nil {
			replacement, ok := declared[mod.Path]
			if !ok || !replacementMatches(mod, replacement) {
				return nil, fmt.Errorf("undeclared replacement for module %s", mod.Path)
			}
		}
	}

	// Exact compares the original module's graph version, even when its source
	// comes from a local directory or a fork with a different version.
	for _, requirement := range plan.Requirements {
		mod, ok := graph[requirement.Path]
		if !ok {
			return nil, fmt.Errorf("required module %s missing from resolved graph", requirement.Path)
		}
		if requirement.Exact && mod.Version != requirement.RequiredVersion {
			return nil, fmt.Errorf("module %s resolved to %s, exact requirement is %s", requirement.Path, mod.Version, requirement.RequiredVersion)
		}
	}

	records := []ReplacementRecord{}
	for _, replacement := range plan.Replacements {
		mod, ok := graph[replacement.Module]
		used := ok && replacementMatches(mod, replacement)
		if ok && (replacement.Version == "" || replacement.Version == mod.Version) && !used {
			return nil, fmt.Errorf("missing explicit replacement for %s", replacement.Module)
		}
		records = append(records, ReplacementRecord{ResolvedReplacement: replacement, Used: used})
	}
	return records, nil
}

func replacementMatches(mod ModuleRecord, replacement ResolvedReplacement) bool {
	target, version := replacement.Source.Directory, ""
	if target == "" {
		target, version = replacement.Source.Module, replacement.TargetVersion
	}
	return mod.Replace != nil &&
		(replacement.Version == "" || replacement.Version == mod.Version) &&
		mod.Replace.Path == target &&
		mod.Replace.Version == version
}

func checkPackages(packages []packageInfo, plan Plan) error {
	graph := map[string]packageInfo{}
	declared := map[string]bool{}
	for _, requirement := range plan.Requirements {
		declared[requirement.Path] = true
	}

	builderPath := reflect.TypeFor[Request]().PkgPath()
	for _, pkg := range packages {
		graph[pkg.ImportPath] = pkg
		if belongsTo(pkg.ImportPath, builderPath) {
			return fmt.Errorf("production package closure contains builder package %s", pkg.ImportPath)
		}
	}

	for _, selected := range plan.Packages {
		pkg, ok := graph[selected.Path]
		if !ok {
			return fmt.Errorf("selected package %s missing from production closure", selected.Path)
		}
		if pkg.Name == "main" || pkg.Module == nil || pkg.Module.Path != selected.Module {
			return fmt.Errorf("selected package %s must be importable and owned by module %s", selected.Path, selected.Module)
		}
	}

	// Generated code may import declared modules directly; their transitive
	// dependencies need no additional declaration in the build request.
	for _, pkg := range packages {
		if pkg.Module == nil || pkg.Module.Path != plan.ModulePath {
			continue
		}
		for _, imported := range pkg.Imports {
			if imported == "C" {
				continue
			}
			dependency, ok := graph[imported]
			if ok && (dependency.Standard || (dependency.Module != nil && dependency.Module.Path == plan.ModulePath)) {
				continue
			}
			if !ok || dependency.Module == nil || !declared[dependency.Module.Path] {
				return fmt.Errorf("generated package %s directly imports %s without a declared package/module source", pkg.ImportPath, imported)
			}
		}
	}
	return nil
}

func normalizedCollections(request Request) Request {
	if request.Packages == nil {
		request.Packages = []Package{}
	}
	if request.Modules == nil {
		request.Modules = []ModuleRequirement{}
	}
	if request.Replacements == nil {
		request.Replacements = []Replacement{}
	}
	return request
}

func sameJSON(a, b any) bool {
	left, _ := jsonBytes(a)
	right, _ := jsonBytes(b)
	return string(left) == string(right)
}

func validToolchain(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}

	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}
