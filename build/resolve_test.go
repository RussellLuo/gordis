package build

import (
	"archive/zip"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func resolveFixture(t *testing.T, request Request) (Plan, error) {
	t.Helper()
	normalized, err := checkRequest(request, valueAdapter())
	if err != nil {
		return Plan{}, err
	}

	r := &runner{ctx: context.Background(), dir: t.TempDir(), env: nativeEnvironment()}
	return resolve(r, normalized, "1.27")
}

func TestSharedLocalQueriesAndOwnership(t *testing.T) {
	for _, name := range []string{
		"explicit",
		"v2 placeholder",
		"query conflict",
		"implicit conflict",
		"source conflict",
		"foreign source",
		"package replace key",
		"nested module",
		"generated identity",
		"hidden replace",
		"version range",
	} {
		t.Run(name, func(t *testing.T) {
			request := localRequest(t)
			want := ""
			switch name {
			case "explicit":
				request.Packages[1].Query = Query{Value: "v1.2.3", Explicit: true}
				request.Modules = []ModuleRequirement{{Path: "example.com/selection", Query: Query{Value: "latest"}, Exact: true}}
			case "v2 placeholder":
				request.Packages = []Package{{Path: "example.com/multi/v2/a", Query: Query{Value: "latest"}, Source: &Source{Directory: "v2"}}}
				request.Replacements = nil
				writeFixture(t, request.Dir, map[string]string{"v2/go.mod": "module example.com/multi/v2\n\ngo 1.27\n", "v2/a/a.go": "package a\n"})
			case "query conflict":
				request.Packages[0].Query = Query{Value: "v1.0.0", Explicit: true}
				request.Packages[1].Query = Query{Value: "v1.1.0", Explicit: true}
				want = "conflicting queries"
			case "implicit conflict":
				request.Packages[1].Query.Value = "main"
				want = "conflicting queries"
			case "source conflict":
				writeFixture(t, request.Dir, map[string]string{"other/go.mod": "module example.com/selection\n\ngo 1.27\n"})
				request.Packages[1].Source = &Source{Directory: "other"}
				want = "source mappings"
			case "foreign source":
				request.Packages[0].Path = "example.com/foreign/a"
				request.Packages[0].Source = &Source{Directory: "source 'tree"}
				want = "does not belong"
			case "package replace key":
				request.Replacements[0].Module += "/a"
				want = "source module mismatch"
			case "nested module":
				writeFixture(t, request.Dir, map[string]string{"source 'tree/nested/go.mod": "module example.com/selection/nested\n\ngo 1.27\n"})
				request.Packages[0].Path = "example.com/selection/nested/a"
				want = "crosses nested module"
			case "generated identity":
				request.ModulePath = "example.com/selection"
				want = "conflicts with input"
			case "hidden replace":
				writeFixture(t, request.Dir, map[string]string{"helper/go.mod": "module example.com/helper\n\ngo 1.27\nreplace example.com/hidden => ../hidden\n"})
				want = "has go.mod replacements"
			case "version range":
				request.Replacements[0].Version = "v1.0.0"
				want = "does not cover selected"
			}

			plan, err := resolveFixture(t, request)
			if want != "" {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("%v; want %s", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			required := plan.Requirements[0]
			if required.QueryExecuted {
				t.Fatal("local selection queried remote", required)
			}
			if name == "explicit" && (required.RequiredVersion != "v1.2.3" || required.DevelopmentPlaceholder || !required.Exact || !required.Query.Explicit) {
				t.Fatal(required)
			}
			if name == "v2 placeholder" && (required.RequiredVersion != "v2.0.0" || !required.DevelopmentPlaceholder) {
				t.Fatal(required)
			}
		})
	}
}

func TestQueryMergeIndependentOfInputOrder(t *testing.T) {
	values := []ModuleRequirement{
		{Query: Query{Value: "main"}},
		{Query: Query{Value: "latest"}},
		{Query: Query{Value: "v1.0.0", Explicit: true}, Exact: true},
	}
	for i := 0; i < len(values); i++ {
		query, exact, err := mergeRequirements(values)
		if err != nil || query != (Query{Value: "v1.0.0", Explicit: true}) || !exact {
			t.Fatal(query, exact, err)
		}
		values = append(values[1:], values[0])
	}
}

// Migrated from the existing graph tests: actual MVS versions and unused
// replacements must be recorded separately from requested require versions.
func TestActualModuleGraphAndExact(t *testing.T) {
	plan := Plan{
		Requirements: []ResolvedRequirement{{Path: "example.com/input", RequiredVersion: "v1.0.0"}},
		Replacements: []ResolvedReplacement{
			{Module: "example.com/input", Source: Source{Directory: "/input"}},
			{Module: "example.com/unused", Source: Source{Directory: "/unused"}},
		},
	}
	modules := []ModuleRecord{{Path: "example.com/input", Version: "v1.1.0", Replace: &ModuleRecord{Path: "/input"}}}
	records, err := checkModules(modules, plan)
	if err != nil || !records[0].Used || records[1].Used {
		t.Fatal(records, err)
	}

	// MVS upgrades are accepted unless the request explicitly pins the graph.
	plan.Requirements[0].Exact = true
	if _, err := checkModules(modules, plan); err == nil || !strings.Contains(err.Error(), "exact") {
		t.Fatal(err)
	}

	// The graph must also preserve declared replacements and required modules.
	plan.Requirements[0].Exact = false
	modules[0].Replace.Path = "/undeclared"
	if _, err := checkModules(modules, plan); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Fatal(err)
	}

	modules[0].Replace = nil
	if _, err := checkModules(modules, plan); err == nil || !strings.Contains(err.Error(), "missing explicit") {
		t.Fatal(err)
	}
	if _, err := checkModules(nil, plan); err == nil || !strings.Contains(err.Error(), "missing from resolved") {
		t.Fatal(err)
	}
}

func fileProxy(t *testing.T) string {
	t.Helper()
	proxy, cache := t.TempDir(), t.TempDir()
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	// Extracted module directories are read-only; restore their permissions
	// before TempDir cleanup removes this isolated cache.
	t.Cleanup(func() {
		if err := filepath.WalkDir(cache, func(file string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(file, 0755)
			}
			return err
		}); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

// Real Go file proxy, adapted from the original package-discovery tests. No
// application or public registry is needed, including with an empty modcache.
func writeProxyModule(t *testing.T, proxy, module, version string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	writeFixture(t, dir, map[string]string{
		version + ".info": fmt.Sprintf(`{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version),
		version + ".mod":  files["go.mod"],
	})
	list := filepath.Join(dir, "list")
	old, _ := os.ReadFile(list)
	if err := os.WriteFile(list, append(old, []byte(version+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}

	file, err := os.Create(filepath.Join(dir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}

	archive := zip.NewWriter(file)
	for name, contents := range files {
		entry, err := archive.Create(module + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedPackageOwnershipQueriesAndMVS(t *testing.T) {
	proxy := fileProxy(t)
	const owner = "example.com/published"
	value := "package value\nfunc Value() string { return \"published\" }\n"
	for _, version := range []string{"v1.0.0", "v1.1.0"} {
		writeProxyModule(t, proxy, owner, version, map[string]string{"go.mod": "module " + owner + "\n\ngo 1.27\n", "a/value.go": value, "b/value.go": value})
	}
	writeProxyModule(t, proxy, "example.com/upgrader", "v1.0.0", map[string]string{
		"go.mod":   "module example.com/upgrader\n\ngo 1.27\nrequire " + owner + " v1.1.0\n",
		"value.go": value,
	})
	writeProxyModule(t, proxy, owner+"/nested", "v0.1.0", map[string]string{"go.mod": "module " + owner + "/nested\n\ngo 1.27\n", "a/value.go": value})
	request := Request{
		ModulePath: "example.com/compiled-test",
		Dir:        t.TempDir(),
		Output:     "program",
		Packages: []Package{
			{Path: owner + "/a", Query: Query{Value: "v1.0.0", Explicit: true}},
			{Path: owner + "/b", Query: Query{Value: "latest"}},
			{Path: "example.com/upgrader", Query: Query{Value: "v1.0.0", Explicit: true}},
			{Path: owner + "/nested/a", Query: Query{Value: "v0.1.0", Explicit: true}},
		},
	}
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	record := readRecord(t, result)
	for _, selected := range record.Plan.Packages {
		if selected.Path == owner+"/a" || selected.Path == owner+"/b" {
			if selected.Module != owner || selected.RequiredVersion != "v1.0.0" {
				t.Fatal(selected)
			}
		}
		if selected.Path == owner+"/nested/a" && selected.Module != owner+"/nested" {
			t.Fatal(selected)
		}
	}

	// The generated require stays at v1.0.0 while Go selects v1.1.0 via upgrader.
	upgraded := false
	for _, mod := range record.Modules {
		if mod.Path == owner {
			upgraded = mod.Version == "v1.1.0"
		}
	}
	if !upgraded {
		t.Fatal(record.Modules)
	}

	queries := 0
	for _, cmd := range record.Commands {
		if len(cmd.Args) == 5 && reflect.DeepEqual(cmd.Args[:4], []string{"go", "list", "-m", "-json"}) && cmd.Args[4] == owner+"@v1.0.0" {
			queries++
		}
	}
	if queries != 1 {
		t.Fatal("shared version queried repeatedly", queries)
	}

	// The same graph must be rejected when the original version is Exact.
	request.Output = "exact"
	request.Modules = []ModuleRequirement{{Path: owner, Query: Query{Value: "v1.0.0", Explicit: true}, Exact: true}}
	if _, err := Build(context.Background(), request, valueAdapter()); err == nil || !strings.Contains(err.Error(), "exact requirement") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}

func TestExplicitQueryOverridesUnavailableImplicitPackage(t *testing.T) {
	proxy := fileProxy(t)
	const owner = "example.com/pinned"
	value := "package value\nfunc Value() string { return \"pinned\" }\n"
	writeProxyModule(t, proxy, owner, "v1.0.0", map[string]string{
		"go.mod":     "module " + owner + "\n\ngo 1.27\n",
		"a/value.go": value, "b/value.go": value,
	})

	// The implicit selection cannot be checked against latest before applying
	// the module's explicit query: a was removed in the latest release.
	writeProxyModule(t, proxy, owner, "v1.1.0", map[string]string{
		"go.mod": "module " + owner + "\n\ngo 1.27\n", "b/value.go": value,
	})
	writeProxyModule(t, proxy, owner+"/nested", "v0.1.0", map[string]string{
		"go.mod": "module " + owner + "/nested\n\ngo 1.27\n", "a/value.go": value,
	})
	for _, mode := range []string{"package", "module", "missing implicit branch"} {
		t.Run(mode, func(t *testing.T) {
			request := Request{
				ModulePath: "example.com/compiled-test",
				Dir:        t.TempDir(),
				Output:     "program",
				Packages:   []Package{{Path: owner + "/a", Query: Query{Value: "latest"}}},
			}
			if mode == "module" {
				request.Modules = []ModuleRequirement{{Path: owner, Query: Query{Value: "v1.0.0", Explicit: true}}}
			} else {
				// Put the explicit package last in both input and lexical order.
				request.Packages = append(request.Packages, Package{Path: owner + "/b", Query: Query{Value: "v1.0.0", Explicit: true}})
				if mode == "missing implicit branch" {
					request.Packages[0].Query.Value = "missing-branch"
				}
			}

			result, err := Build(context.Background(), request, valueAdapter())
			if err != nil {
				t.Fatal(err)
			}

			record := readRecord(t, result)
			if len(record.Plan.Requirements) != 1 || record.Plan.Requirements[0].Query != (Query{Value: "v1.0.0", Explicit: true}) {
				t.Fatal(record.Plan.Requirements)
			}

			for _, selected := range record.Plan.Packages {
				if selected.Module != owner || selected.RequiredVersion != "v1.0.0" {
					t.Fatal(selected)
				}
			}
		})
	}

	t.Run("parent query does not select nested module version", func(t *testing.T) {
		request := Request{
			ModulePath: "example.com/compiled-test",
			Dir:        t.TempDir(),
			Output:     "program",
			Packages: []Package{
				{Path: owner + "/nested/a", Query: Query{Value: "latest"}},
				{Path: owner + "/b", Query: Query{Value: "v1.0.0", Explicit: true}},
			},
		}
		result, err := Build(context.Background(), request, valueAdapter())
		if err != nil {
			t.Fatal(err)
		}

		record := readRecord(t, result)
		if len(record.Plan.Requirements) != 2 {
			t.Fatal(record.Plan.Requirements)
		}

		for _, selected := range record.Plan.Packages {
			if selected.Path == owner+"/nested/a" && (selected.Module != owner+"/nested" || selected.RequiredVersion != "v0.1.0") {
				t.Fatal("parent module swallowed nested selection", selected)
			}
		}
	})
}

func TestUnusedVersionedLocalReplacement(t *testing.T) {
	proxy := fileProxy(t)
	writeProxyModule(t, proxy, "example.com/helper", "v1.1.0", map[string]string{
		"go.mod":   "module example.com/helper\n\ngo 1.27\n",
		"value.go": "package helper\nconst Value = \"published\"\n",
	})
	request := localRequest(t)
	writeFixture(t, request.Dir, map[string]string{
		"source 'tree/go.mod": "module example.com/selection\n\ngo 1.27\nrequire example.com/helper v1.1.0\n",
	})
	request.Replacements[1].Version = "v1.0.0"
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	record := readRecord(t, result)
	for _, replacement := range record.Replacements {
		if replacement.Module == "example.com/helper" && replacement.Used {
			t.Fatal("versioned replacement incorrectly used", replacement)
		}
	}

	found := false
	for _, input := range record.Inputs {
		if input.Module == "example.com/helper" {
			found = true
			if len(input.Files) != 1 || input.Files["go.mod"] == "" {
				t.Fatal("unused mapping should only record its module file", input)
			}
		}
	}
	if !found {
		t.Fatal("unused mapping omitted from input records")
	}
}

func TestRemoteForkMappingsAndExplicitLocalMovingQuery(t *testing.T) {
	proxy := fileProxy(t)
	const owner, fork = "example.com/original", "example.com/fork"
	value := "package value\nfunc Value() string { return \"remote\" }\n"
	writeProxyModule(t, proxy, owner, "v1.0.0", map[string]string{"go.mod": "module " + owner + "\n\ngo 1.27\n", "a/value.go": value, "b/value.go": value})
	writeProxyModule(t, proxy, fork, "v1.0.0", map[string]string{
		"go.mod":     "module " + fork + "\n\ngo 1.27\n",
		"a/value.go": value,
		"b/value.go": value,
		"value.go":   value,
	})
	for _, mode := range []string{"module", "inline", "root"} {
		t.Run(mode, func(t *testing.T) {
			request := Request{
				ModulePath: "example.com/compiled-test",
				Dir:        t.TempDir(),
				Output:     "program",
				Packages: []Package{
					{Path: owner + "/a", Query: Query{Value: "v1.0.0", Explicit: true}},
					{Path: owner + "/b", Query: Query{Value: "latest"}},
				},
			}
			source := Source{Module: fork, Query: "latest"}
			if mode == "module" {
				request.Replacements = []Replacement{{Module: owner, Source: source}}
			} else {
				for i := range request.Packages {
					request.Packages[i].Source = &source
				}
			}
			if mode == "root" {
				request.Packages = []Package{{Path: owner, Query: Query{Value: "v1.0.0", Explicit: true}, Source: &source}}
			}

			result, err := Build(context.Background(), request, valueAdapter())
			if err != nil {
				t.Fatal(err)
			}

			record := readRecord(t, result)
			if len(record.Replacements) != 1 ||
				!record.Replacements[0].Used ||
				!record.Replacements[0].QueryExecuted ||
				record.Replacements[0].TargetVersion != "v1.0.0" ||
				record.Replacements[0].Module != owner {
				t.Fatal(record.Replacements)
			}

			for _, selected := range record.Plan.Packages {
				if selected.Module != owner {
					t.Fatal(selected)
				}
			}
		})
	}

	// A local source does not fabricate the published identity of latest.
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source/go.mod": "module " + owner + "\n\ngo 1.27\n", "source/a/value.go": value})
	request := Request{
		ModulePath: "example.com/compiled-test",
		Dir:        root,
		Output:     "program",
		Packages: []Package{
			{
				Path:   owner + "/a",
				Query:  Query{Value: "latest", Explicit: true},
				Source: &Source{Directory: "source"},
			},
		},
	}
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}

	required := readRecord(t, result).Plan.Requirements[0]
	if !required.QueryExecuted || required.DevelopmentPlaceholder || required.RequiredVersion != "v1.0.0" {
		t.Fatal(required)
	}
}
