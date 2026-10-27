// Package buildconfig adapts this application's registrations to the public SDK.
// It is used by the build recipe, never by the generated runtime.
package buildconfig

import (
	"fmt"
	"sort"
	"strings"
	"time"

	builder "github.com/RussellLuo/gordis/build"
)

const modulePath = "example.com/gordis-build-custom"

// Recipe builds the application in dir. Third-party module versions come from
// the application's go.mod; only the application's own source is mapped locally.
func Recipe(dir, output string) (builder.Request, builder.Adapter) {
	query := builder.Query{Value: "latest"}
	request := builder.Request{
		ModulePath: "example.com/custom-compiled",
		Dir:        dir,
		Output:     output,
		Packages: []builder.Package{
			{Path: modulePath + "/app", Query: query},
			{Path: modulePath + "/plugins/hello", Query: query},
			{Path: modulePath + "/plugins/chinese", Query: query},
		},
		Replacements: []builder.Replacement{
			{Module: modulePath, Source: builder.Source{Directory: dir}},
		},
	}
	return request, builder.Adapter{
		Generate: generateCustom,
		Validate: builder.Validation{Args: []string{"--verify-assembly"}, Timeout: 5 * time.Second},
	}
}

func generateCustom(plan builder.Plan) (builder.Project, error) {
	packages := append([]builder.ResolvedPackage{}, plan.Packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })

	var code strings.Builder
	code.WriteString("package main\nimport (\n\"fmt\"\n\"os\"\n")
	appFound := false
	var registrations []string
	for i, pkg := range packages {
		if pkg.Path == modulePath+"/app" {
			fmt.Fprintf(&code, "app %q\n", pkg.Path)
			appFound = true
			continue
		}
		fmt.Fprintf(&code, "p%d %q\n", i, pkg.Path)
		registrations = append(registrations, fmt.Sprintf("p%d.Registration()", i))
	}
	if !appFound || len(registrations) == 0 {
		return builder.Project{}, fmt.Errorf("custom recipe needs app and registration packages")
	}
	fmt.Fprintf(&code, `)
func main() {
	application, err := app.Compose(%s)
	if err == nil { err = app.Run(os.Args[1:], application) }
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
`, strings.Join(registrations, ", "))
	return builder.Project{Files: map[string][]byte{"main.go": []byte(code.String())}}, nil
}
