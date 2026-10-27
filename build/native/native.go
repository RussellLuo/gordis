// Package native adapts ordinary Gordis Plugin collections to the build engine.
// Applications own defaults, configuration, lifecycle and a pure --check mode.
package native

import (
	"fmt"
	"sort"
	"strings"

	builder "github.com/RussellLuo/gordis/build"
)

// Command returns the fixed gordis-build CLI. It uses app.Run and selected
// packages' Plugins functions; other assembly models can use build.Run/Build.
func Command() builder.Command {
	return builder.Command{
		Name:          "gordis-build",
		DefaultOutput: "gordis-app",
		AcceptApp:     true,
		Help: `--app is required; no positional arguments.
Application: func Run([]string, []gordis.Plugin) error, supporting pure --check.
Selected packages: func Plugins() []gordis.Plugin. Defaults belong to the application.`,
		Prepare: prepare,
	}
}

func prepare(args builder.Arguments) (builder.Request, builder.Adapter, error) {
	if args.App == nil {
		return builder.Request{}, builder.Adapter{}, fmt.Errorf("--app is required")
	}
	if len(args.Positionals) != 0 {
		return builder.Request{}, builder.Adapter{}, fmt.Errorf("unexpected positional arguments: %v", args.Positionals)
	}

	for _, pkg := range args.Request.Packages {
		if pkg.Path == args.App.Path {
			return builder.Request{}, builder.Adapter{}, fmt.Errorf("application package %s cannot also be selected with --with", pkg.Path)
		}
	}

	request := args.Request
	request.ModulePath = "example.com/gordis-compiled"
	request.Packages = append(append([]builder.Package{}, request.Packages...), *args.App)
	request.Modules = append(append([]builder.ModuleRequirement{}, request.Modules...), builder.ModuleRequirement{
		Path:  "github.com/RussellLuo/gordis",
		Query: builder.Query{Value: "latest"},
	})
	app := args.App.Path
	return request, builder.Adapter{
		Generate: func(plan builder.Plan) (builder.Project, error) { return generate(plan, app) },
		Validate: builder.Validation{Args: []string{"--check"}},
	}, nil
}

func generate(plan builder.Plan, app string) (builder.Project, error) {
	packages := append([]builder.ResolvedPackage{}, plan.Packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })

	var code strings.Builder
	fmt.Fprintf(&code, `package main
import (
"fmt"
"os"
"github.com/RussellLuo/gordis"
app %q
`, app)
	found := false
	factories := []string{}
	for i, pkg := range packages {
		if pkg.Path == app {
			found = true
			continue
		}
		alias := fmt.Sprintf("p%d", i)
		fmt.Fprintf(&code, "%s %q\n", alias, pkg.Path)
		factories = append(factories, alias)
	}
	if !found {
		return builder.Project{}, fmt.Errorf("application package %s missing from plan", app)
	}

	// Explicit function types let the Go compiler check the exact factory
	// and application signatures, including rejection of variadic factories.
	code.WriteString(`)
func main() {
var plugins []gordis.Plugin
`)
	for _, alias := range factories {
		fmt.Fprintf(&code, `var factory%s func() []gordis.Plugin = %s.Plugins
plugins = append(plugins, factory%s()...)
`, alias, alias, alias)
	}
	code.WriteString(`var run func([]string, []gordis.Plugin) error = app.Run
if err := run(os.Args[1:], plugins); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
`)
	return builder.Project{Files: map[string][]byte{"main.go": []byte(code.String())}}, nil
}
