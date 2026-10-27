# Development checks

English | [中文](development.zh.md) · [README](../README.md)

Core, build and the examples are separate Go modules. A test run from the
repository root does not include the nested modules.

## Build module

From `build/`, run these checks independently of the core module:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
```

## Examples against local source

Run from `build/`. The temporary workspace lets the example tests use this
checkout before the build module is published. The explicit version replacements
apply only to this workspace, including the unpublished development placeholder;
normal users obtain dependencies through go.mod without these mappings.

`go work edit -replace` edits the temporary go.work, leaving the examples'
go.mod files unchanged. The generated application's own build still uses
`GOWORK=off`; its source mappings are supplied explicitly by the request or
test fixture.

Create the workspace and check both examples:

```sh
check_dir=$(mktemp -d)
core_dir=$(cd .. && pwd)
export GOWORK="$check_dir/go.work"
go work init "$core_dir" "$PWD" "$PWD/examples/minimal" "$PWD/examples/custom"
go work edit -replace "github.com/RussellLuo/gordis@v0.0.0-20261014054822-1b6675a88714=$core_dir"
go work edit -replace "github.com/RussellLuo/gordis/build@v0.0.0=$PWD"
go test ./examples/minimal/... ./examples/custom/...
unset GOWORK
```

The workspace maps dependency versions only for source checks and does not
modify input module files. Tests use local modules and a file-based Go proxy
to cover resolution, generation, validation, records and delivery. Native and
custom integration tests also check plugin activation, configuration and the
production package closure. The generated runtime does not carry the builder.
