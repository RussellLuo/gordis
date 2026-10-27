package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Run parses standard build arguments, applies application policy once, and
// calls Build. Help never calls Prepare or inspects sources. Exit codes are
// 0 for help/success, 2 for arguments/Prepare, and 1 for Build failure.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, command Command) int {
	if helpRequested(args, command.AcceptApp) {
		fmt.Fprintf(stdout, "Usage: %s [application arguments] [--with package[@query][=replacement] ...]\n", command.Name)
		fmt.Fprintln(stdout, "  --replace module[@fixed-version]=replacement  replace a dependency without selecting a package")
		fmt.Fprintln(stdout, "  --with package[@query][=replacement]          select an importable package")
		if command.AcceptApp {
			fmt.Fprintln(stdout, "  --app package[@query][=replacement]           application package")
		}
		fmt.Fprintf(stdout, "  --output file                               output (default: %s)\n", platformOutput(command.DefaultOutput))
		fmt.Fprintln(stdout, "  -h, --help                                   show help without accessing sources")
		if command.Help != "" {
			fmt.Fprintln(stdout, command.Help)
		}
		return 0
	}

	arguments, err := parseArguments(args, command)
	if err == nil && command.Prepare == nil {
		err = fmt.Errorf("Prepare is required")
	}

	var request Request
	var adapter Adapter
	if err == nil {
		if err = ctx.Err(); err == nil {
			request, adapter, err = command.Prepare(arguments)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command.Name, err)
		return 2
	}

	result, err := Build(ctx, request, adapter)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command.Name, err)
		return 1
	}
	fmt.Fprintf(stdout, "Built %s\nBuild record: %s\n", request.Output, result.RecordPath)
	return 0
}

func platformOutput(output string) string {
	if runtime.GOOS == "windows" && output != "" && !strings.HasSuffix(strings.ToLower(output), ".exe") {
		return output + ".exe"
	}
	return output
}

// Scan for help before parsing or inspecting sources. A flag's value and
// everything after -- are data, even if their spelling is --help.
func helpRequested(args []string, acceptApp bool) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "-h" || args[i] == "--help" {
			return true
		}
		switch args[i] {
		case "--with", "-with", "--replace", "-replace", "--output", "-output":
			i++
		case "--app", "-app":
			if acceptApp {
				i++
			}
		}
	}
	return false
}

func parseArguments(args []string, command Command) (Arguments, error) {
	a := Arguments{Args: append([]string{}, args...), Positionals: []string{}}
	cwd, err := os.Getwd()
	if err != nil {
		return a, err
	}

	a.Request = Request{
		Dir:          cwd,
		Output:       platformOutput(command.DefaultOutput),
		Packages:     []Package{},
		Modules:      []ModuleRequirement{},
		Replacements: []Replacement{},
	}
	seenPackages, seenReplacements := map[string]bool{}, map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			a.Positionals = append(a.Positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			a.Positionals = append(a.Positionals, arg)
			continue
		}

		// Split only the first equals sign: the value may itself contain an
		// inline package-to-source mapping.
		flag, value, supplied := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if flag != "with" && flag != "replace" && flag != "output" && !(command.AcceptApp && flag == "app") {
			return a, fmt.Errorf("unknown option %s", arg)
		}
		if !supplied {
			i++
			if i == len(args) {
				return a, fmt.Errorf("--%s requires a value", flag)
			}
			value = args[i]
		}
		if value == "" {
			return a, fmt.Errorf("--%s requires a nonempty value", flag)
		}

		switch flag {
		case "output":
			if a.OutputSet {
				return a, fmt.Errorf("duplicate --output")
			}
			a.OutputSet, a.Request.Output = true, value
		case "app", "with":
			p, err := parsePackage(value)
			if err != nil {
				return a, err
			}
			if flag == "app" {
				if a.App != nil {
					return a, fmt.Errorf("duplicate --app")
				}
				a.App = &p
			} else {
				if seenPackages[p.Path] {
					return a, fmt.Errorf("duplicate --with package %s", p.Path)
				}
				seenPackages[p.Path] = true
				a.Request.Packages = append(a.Request.Packages, p)
			}
		case "replace":
			p, err := parsePackage(value)
			if err != nil {
				return a, err
			}
			if p.Source == nil {
				return a, fmt.Errorf("--replace requires module[@fixed-version]=replacement")
			}
			if seenReplacements[p.Path] {
				return a, fmt.Errorf("duplicate --replace key %s", p.Path)
			}
			seenReplacements[p.Path] = true
			replacement := Replacement{Module: p.Path, Source: *p.Source}
			if p.Query.Explicit {
				if !fixedVersion(p.Query.Value) {
					return a, fmt.Errorf("--replace left version must be fixed: %s", p.Query.Value)
				}
				replacement.Version = p.Query.Value
			}
			a.Request.Replacements = append(a.Request.Replacements, replacement)
		}
	}
	sort.Slice(a.Request.Packages, func(i, j int) bool { return a.Request.Packages[i].Path < a.Request.Packages[j].Path })
	sort.Slice(a.Request.Replacements, func(i, j int) bool { return a.Request.Replacements[i].Module < a.Request.Replacements[j].Module })
	return a, nil
}

func parsePackage(value string) (Package, error) {
	left, target, replaced := strings.Cut(value, "=")
	path, query, explicit := strings.Cut(left, "@")
	if !explicit {
		query = "latest"
	}

	p := Package{Path: path, Query: Query{Value: query, Explicit: explicit}}
	if err := checkPackage(path); err != nil {
		return p, err
	}
	if err := checkQuery(p.Query); err != nil {
		return p, fmt.Errorf("package %s: %w", path, err)
	}
	if replaced {
		if target == "" {
			return p, fmt.Errorf("package %s: empty replacement", path)
		}
		source, err := parseSource(target)
		if err != nil {
			return p, err
		}
		p.Source = &source
	}
	return p, nil
}

func parseSource(target string) (Source, error) {
	local := filepath.IsAbs(target) || target == "." || target == ".." ||
		strings.HasPrefix(target, "."+string(filepath.Separator)) ||
		strings.HasPrefix(target, ".."+string(filepath.Separator))
	if !local {
		// Compatibility for existing bare relative directories; no go.mod is read.
		info, err := os.Stat(target)
		local = err == nil && info.IsDir()
	}
	if local {
		return Source{Directory: target}, nil
	}

	path, query, explicit := strings.Cut(target, "@")
	if !explicit {
		query = "latest"
	}

	source := Source{Module: path, Query: query}
	if err := checkSource(source); err != nil {
		return Source{}, fmt.Errorf("replacement %q: %w", target, err)
	}
	return source, nil
}
