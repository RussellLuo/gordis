# Command and SDK usage

English | [中文](usage.zh.md) · [README](../README.md)

## Native command

The native command requires `--app package[@query][=replacement]`, accepts optional `--with` packages and shares `--replace`/`--output` with the SDK CLI. The application exports `func Run([]string, []gordis.Plugin) error` and supports a pure `--check`; selected plugin packages export `func Plugins() []gordis.Plugin`. Defaults and runtime lifecycle belong to the application. Signatures are checked by the Go compiler, duplicate type IDs by the application's Host registration. The command uses the public record format and defaults to `./gordis-app` (`.exe` on Windows). See [minimal's usage and release status](../examples/minimal/README.md).

## Build API

```go
import builder "github.com/RussellLuo/gordis/build"

result, err := builder.Build(ctx, builder.Request{
	ModulePath: "example.com/compiled-app",
	Dir:        cwd,
	Output:     "dist/app",
	Packages: []builder.Package{{
		Path:  "example.com/myapp/run",
		Query: builder.Query{Value: "latest"},
	}},
	Replacements: []builder.Replacement{{
		Module: "example.com/myapp",
		Source: builder.Source{Directory: "./app-source"},
	}},
}, builder.Adapter{
	Generate: generateMain,
	Validate: builder.Validation{Args: []string{"--verify"}},
})
```

In this sketch, the application implements `generateMain(Plan) (Project, error)`, returning root Go main code that imports the selected package, and its generated executable implements `--verify` without starting business work. The engine does not interpret factory names or application registration types. A nil Record callback uses `gordis.build-record/v1alpha1`; a custom callback receives Facts and must return a valid JSON object in `record.json`. See [record layout and encoding](records.md).

`Run(ctx, args, stdout, stderr, Command)` shares `--with`, `--replace`, `--output`, help, diagnostics and exit codes. Command.Prepare adds application defaults and returns Request/Adapter, then Run calls the same Build. `AcceptApp` enables parsing `--app` separately; application policy assigns its role. Help returns before Prepare, source inspection or Go commands. Exit codes: 0 success/help, 2 arguments/Prepare, 1 Build failure.

Packages select importable code; Modules add requirements without selecting factories; Replacements only change source. Source is either Directory or Module + Query. An implicit local query uses a development version without a release query; an explicit moving query still resolves the original module identity. Plan distinguishes require versions from Facts.Modules' actual MVS versions; Exact rejects upgrades.

Generate and Record are trusted pure callbacks receiving detached snapshots. Validation has a default 30-second timeout, bounded by the outer context. Errors include their stage and unwrap the original cause. Only after validation, input rechecking and record encoding succeed does Build deliver the binary and `<output>.build/`; it refuses either existing path and cleans its own partial delivery on normal errors.

The build uses the local Go toolchain, current OS/architecture, `CGO_ENABLED=0`, `GOWORK=off`, empty `GOFLAGS` and `GOTOOLCHAIN=local`. Runtime output does not depend on the builder, source, record directory or Go executable. Input hashes compare snapshots, not an isolated filesystem; callbacks and candidate checks do not run in a process sandbox.
