package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func fixedVersion(value string) bool {
	return semver.IsValid(value) && module.CanonicalVersion(value) == value
}

func checkPackage(path string) error {
	if err := module.CheckImportPath(path); err != nil {
		return err
	}
	if !strings.Contains(strings.Split(path, "/")[0], ".") {
		return fmt.Errorf("package %q must have a module-qualified import path", path)
	}
	return nil
}

func checkQuery(query Query) error {
	if query.Value == "" || strings.ContainsAny(query.Value, "@= \t\r\n\x00") {
		return fmt.Errorf("invalid query %q", query.Value)
	}
	return nil
}

func checkSource(source Source) error {
	if source.Directory != "" {
		if source.Module != "" || source.Query != "" || strings.ContainsRune(source.Directory, 0) {
			return fmt.Errorf("source must contain only Directory or Module + Query")
		}
		return nil
	}
	if err := module.CheckPath(source.Module); err != nil {
		return err
	}
	return checkQuery(Query{Value: source.Query})
}

func belongsTo(entry, owner string) bool {
	return entry == owner || strings.HasPrefix(entry, owner+"/")
}

func developmentVersion(path string) string {
	_, major, _ := module.SplitPathVersion(path)
	if prefix := module.PathMajorPrefix(major); prefix != "" {
		return prefix + ".0.0"
	}
	return "v0.0.0"
}

type resolver struct {
	r       *runner
	plan    Plan
	queries map[string]ModuleRecord
	probes  int
}

func (s *resolver) probe() (string, error) {
	s.probes++
	dir := filepath.Join(s.r.dir, fmt.Sprintf("probe-%d", s.probes))
	plan := Plan{ModulePath: fmt.Sprintf("%s/probe%d", s.plan.ModulePath, s.probes), GoVersion: s.plan.GoVersion}
	raw, err := moduleFile(plan)
	if err != nil {
		return "", err
	}
	return dir, writeFile(dir, "go.mod", raw)
}

// Version queries intentionally run without replacement mappings. An explicit
// moving query on local source must still resolve the original published identity.
func (s *resolver) query(path, query string) (ModuleRecord, error) {
	key := path + "@" + query
	if result, ok := s.queries[key]; ok {
		return result, nil
	}

	dir, err := s.probe()
	if err != nil {
		return ModuleRecord{}, err
	}

	raw, err := s.r.run("resolve", dir, "go", "list", "-m", "-json", key)
	if err != nil {
		return ModuleRecord{}, fmt.Errorf("module %s: %w", key, err)
	}

	var result ModuleRecord
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.Path != path || !fixedVersion(result.Version) {
		return result, fmt.Errorf("module query %s resolved outside requested identity/version: %s@%s", key, result.Path, result.Version)
	}

	s.queries[key] = result
	return result, nil
}

func localModule(dir, base string) (string, string, error) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}

	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", err
	}

	file := filepath.Join(dir, "go.mod")
	info, err := os.Stat(file)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("source %s must contain a regular go.mod", dir)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}

	metadata, err := modfile.Parse(file, raw, nil)
	if err != nil {
		return "", "", err
	}
	if metadata.Module == nil {
		return "", "", fmt.Errorf("source %s has no module identity", dir)
	}
	if len(metadata.Replace) != 0 {
		return "", "", fmt.Errorf("source %s has go.mod replacements; declare dependencies explicitly in Request.Replacements", dir)
	}
	return dir, metadata.Module.Mod.Path, nil
}

func (s *resolver) replacement(replacement Replacement, base string) (ResolvedReplacement, error) {
	resolved := ResolvedReplacement{Module: replacement.Module, Version: replacement.Version, Source: replacement.Source}
	if replacement.Source.Directory != "" {
		dir, owner, err := localModule(replacement.Source.Directory, base)
		if err != nil {
			return resolved, fmt.Errorf("source for module %s: %w", replacement.Module, err)
		}
		if owner != replacement.Module {
			return resolved, fmt.Errorf("source module mismatch: replacement %s, go.mod declares %s", replacement.Module, owner)
		}
		resolved.Source.Directory = dir
		return resolved, nil
	}

	target, err := s.query(replacement.Source.Module, replacement.Source.Query)
	if err != nil {
		return resolved, fmt.Errorf("replacement for %s: %w", replacement.Module, err)
	}

	resolved.TargetVersion, resolved.QueryExecuted = target.Version, true
	return resolved, nil
}

func mergeReplacement(values *[]ResolvedReplacement, replacement ResolvedReplacement) error {
	for _, old := range *values {
		if old.Module == replacement.Module {
			if old != replacement {
				return fmt.Errorf("conflicting or overlapping source mappings for module %s", replacement.Module)
			}
			return nil
		}
	}

	*values = append(*values, replacement)
	sort.Slice(*values, func(i, j int) bool { return (*values)[i].Module < (*values)[j].Module })
	return nil
}

// matchingReplacement prefers the deepest module path, allowing an explicit
// nested-module mapping to take precedence over its parent.
func matchingReplacement(entry string, values []ResolvedReplacement) *ResolvedReplacement {
	var best *ResolvedReplacement
	for i := range values {
		if belongsTo(entry, values[i].Module) && (best == nil || len(values[i].Module) > len(best.Module)) {
			best = &values[i]
		}
	}
	return best
}

func checkNestedModule(entry string, replacement ResolvedReplacement) error {
	dir := replacement.Source.Directory
	if dir == "" || entry == replacement.Module {
		return nil
	}

	tail := strings.TrimPrefix(entry, replacement.Module+"/")
	for _, component := range strings.Split(tail, "/") {
		dir = filepath.Join(dir, component)
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return fmt.Errorf("package %s crosses nested module at %s; supply that module's source mapping", entry, dir)
		}
	}
	return nil
}

// Discover the deepest module identity without requiring this package to exist
// at its unmerged query. Package existence/importability is checked by Go in
// the final project, after explicit queries have overridden implicit defaults.
func (s *resolver) discover(pkg Package, replacements []ResolvedReplacement, requirements map[string][]ModuleRequirement) (string, error) {
	if mapping := matchingReplacement(pkg.Path, replacements); mapping != nil {
		if err := checkNestedModule(pkg.Path, *mapping); err != nil {
			return "", err
		}
		return mapping.Module, nil
	}

	var lastErr error
	for candidate := pkg.Path; candidate != ""; {
		if module.CheckPath(candidate) == nil {
			query := pkg.Query
			known := requirements[candidate]
			if len(known) > 0 {
				var err error
				values := append([]ModuleRequirement{{Path: candidate, Query: pkg.Query}}, known...)
				query, _, err = mergeRequirements(values)
				if err != nil {
					return "", fmt.Errorf("module %s: %w", candidate, err)
				}
			}
			mod, err := s.query(candidate, query.Value)
			if err == nil {
				return mod.Path, nil
			}
			if len(known) > 0 || s.r.ctx.Err() != nil {
				return "", fmt.Errorf("package %s ownership: %w", pkg.Path, err)
			}
			lastErr = err
		}
		end := strings.LastIndexByte(candidate, '/')
		if end < 0 {
			break
		}
		candidate = candidate[:end]
	}
	return "", fmt.Errorf("package %s ownership: %w", pkg.Path, lastErr)
}

func resolve(r *runner, request Request, goVersion string) (Plan, error) {
	s := resolver{
		r: r,
		plan: Plan{
			ModulePath:   request.ModulePath,
			GoVersion:    goVersion,
			Packages:     []ResolvedPackage{},
			Requirements: []ResolvedRequirement{},
			Replacements: []ResolvedReplacement{},
		},
		queries: map[string]ModuleRecord{},
	}
	for _, replacement := range request.Replacements {
		resolved, err := s.replacement(replacement, request.Dir)
		if err != nil {
			return s.plan, err
		}
		if err := mergeReplacement(&s.plan.Replacements, resolved); err != nil {
			return s.plan, err
		}
	}

	packages := append([]Package{}, request.Packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })

	// Inline local mappings are known without any published package query.
	for _, pkg := range packages {
		if pkg.Source == nil || pkg.Source.Directory == "" {
			continue
		}
		dir, owner, err := localModule(pkg.Source.Directory, request.Dir)
		if err != nil {
			return s.plan, fmt.Errorf("package %s source: %w", pkg.Path, err)
		}
		if !belongsTo(pkg.Path, owner) {
			return s.plan, fmt.Errorf("package %s does not belong to source module %s", pkg.Path, owner)
		}
		if err := mergeReplacement(&s.plan.Replacements, ResolvedReplacement{Module: owner, Source: Source{Directory: dir}}); err != nil {
			return s.plan, err
		}
	}

	grouped := map[string][]ModuleRequirement{}
	for _, required := range request.Modules {
		grouped[required.Path] = append(grouped[required.Path], required)
	}

	// Discover explicit package queries first, regardless of input/path order,
	// so sibling implicit selections can use the known module's explicit query.
	discovery := append([]Package{}, packages...)
	sort.SliceStable(discovery, func(i, j int) bool {
		return discovery[i].Query.Explicit && !discovery[j].Query.Explicit
	})
	owners := map[string]string{}
	for _, pkg := range discovery {
		owner, err := s.discover(pkg, s.plan.Replacements, grouped)
		if err != nil {
			return s.plan, err
		}
		owners[pkg.Path] = owner
		grouped[owner] = append(grouped[owner], ModuleRequirement{Path: owner, Query: pkg.Query})
	}

	// All owners are known before adding inferred forks: a sibling's inline
	// mapping must not hide an independently owned nested module.
	for _, pkg := range packages {
		if pkg.Source == nil || pkg.Source.Directory != "" {
			continue
		}
		resolved, err := s.replacement(Replacement{Module: owners[pkg.Path], Source: *pkg.Source}, request.Dir)
		if err != nil {
			return s.plan, err
		}
		if err := mergeReplacement(&s.plan.Replacements, resolved); err != nil {
			return s.plan, err
		}
	}

	for _, pkg := range packages {
		s.plan.Packages = append(s.plan.Packages, ResolvedPackage{Path: pkg.Path, Module: owners[pkg.Path]})
	}

	paths := []string{}
	for path := range grouped {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		query, exact, err := mergeRequirements(grouped[path])
		if err != nil {
			return s.plan, fmt.Errorf("module %s: %w", path, err)
		}
		mapping := matchingReplacement(path, s.plan.Replacements)
		local := mapping != nil && mapping.Module == path && mapping.Source.Directory != ""

		// Implicit local selections need only a valid development version.
		// Explicit moving queries still resolve the original published module;
		// explicit fixed versions can use local source without a query.
		placeholder := local && !query.Explicit
		queried := !placeholder && !(local && fixedVersion(query.Value))
		version := query.Value
		if placeholder {
			version = developmentVersion(path)
		} else if queried {
			resolved, err := s.query(path, query.Value)
			if err != nil {
				return s.plan, err
			}
			version = resolved.Version
		}

		if err := module.Check(path, version); err != nil {
			return s.plan, err
		}
		if mapping != nil && mapping.Module == path && mapping.Version != "" && mapping.Version != version {
			return s.plan, fmt.Errorf("replacement version %s does not cover selected %s@%s", mapping.Version, path, version)
		}
		s.plan.Requirements = append(s.plan.Requirements, ResolvedRequirement{
			Path:                   path,
			Query:                  query,
			RequiredVersion:        version,
			QueryExecuted:          queried,
			DevelopmentPlaceholder: placeholder,
			Exact:                  exact,
		})
		for i := range s.plan.Packages {
			if s.plan.Packages[i].Module == path {
				s.plan.Packages[i].RequiredVersion = version
			}
		}
	}

	for _, required := range s.plan.Requirements {
		if required.Path == s.plan.ModulePath {
			return s.plan, fmt.Errorf("generated module %s conflicts with input module", s.plan.ModulePath)
		}
	}

	for _, replacement := range s.plan.Replacements {
		if replacement.Module == s.plan.ModulePath || replacement.Source.Module == s.plan.ModulePath {
			return s.plan, fmt.Errorf("generated module %s conflicts with source module", s.plan.ModulePath)
		}
	}
	return s.plan, nil
}

// Explicit queries override all implicit defaults for the same module. Queries
// within the winning group must agree; Exact survives either group's override.
func mergeRequirements(values []ModuleRequirement) (Query, bool, error) {
	explicit := map[string]bool{}
	implicit := map[string]bool{}
	exact := false
	for _, value := range values {
		exact = exact || value.Exact
		if value.Query.Explicit {
			explicit[value.Query.Value] = true
		} else {
			implicit[value.Query.Value] = true
		}
	}

	queries := explicit
	if len(queries) == 0 {
		queries = implicit
	}
	if len(queries) != 1 {
		return Query{}, false, fmt.Errorf("conflicting queries (explicit=%v, implicit=%v)", explicit, implicit)
	}

	for query := range queries {
		return Query{Value: query, Explicit: len(explicit) != 0}, exact, nil
	}
	panic("unreachable")
}

func moduleFile(plan Plan) ([]byte, error) {
	file := new(modfile.File)
	if err := file.AddModuleStmt(plan.ModulePath); err != nil {
		return nil, err
	}
	if err := file.AddGoStmt(plan.GoVersion); err != nil {
		return nil, err
	}

	for _, requirement := range plan.Requirements {
		if err := file.AddRequire(requirement.Path, requirement.RequiredVersion); err != nil {
			return nil, err
		}
	}

	for _, replacement := range plan.Replacements {
		target, version := replacement.Source.Directory, ""
		if target == "" {
			target, version = replacement.Source.Module, replacement.TargetVersion
		}
		if err := file.AddReplace(replacement.Module, replacement.Version, target, version); err != nil {
			return nil, err
		}
	}
	file.SortBlocks()
	file.Cleanup()
	return file.Format()
}
