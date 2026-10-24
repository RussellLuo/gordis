# Development and verification

English | [中文](development.zh.md) · [README](../README.md)

Run these commands from `rpc/` with a Go toolchain matching [go.mod](../go.mod). Set `GOCACHE=/tmp/gordis-rpc-cache` if you need a writable cache.

The module pins `github.com/RussellLuo/jsonschema-go v0.0.0-20261020143036-d0e0ff947dfa` with jsonschemax support. Go downloads it through normal module resolution; no adjacent jsonschema-go checkout or local replace is needed.

## Common checks

Core and RPC are separate modules: a root `go test ./...` does not test `rpc/`. Run Core checks from the Gordis root. From `rpc/`, the commands below create a temporary workspace using both source modules; test subprocess builds inherit this mapping.

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
# Runtime, generator and real child-process regressions.
go test -race -count=1 -timeout=180s ./...

# Only public examples: real Host/child and Local/process comparisons.
go test -race -count=1 ./examples/...

# Regenerate library fixtures after generator or contract changes.
go generate ./internal/genfixture/contract ./internal/streamfixture/contract
```

After generation, check consistency without writing:

```sh
cd internal/genfixture/contract
go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go -check
```

See [Bindings](bindings.md) and [basic](../examples/basic/README.md) for basic generation and run commands. Commit generated files; consumers should not need generation during builds.

## What each check establishes

Module tests cover configuration declaration, Schema adaptation, defaults, strict decoding, mapping and isolation; DescribeProgram reaping without initialization; Host validation before Initialize and independent child validation; Unit/boundary rejection, handshake checks, startup deadlines, export validation cancellation, real subprocess calls and callbacks, stream cancellation, activation rollback and reaping. They test the adapter's responsibilities, not a second copy of jsonschema-go's inference/constraint matrix or full meta-schema validity. Fixtures live in the module without downstream repository dependencies. Example tests also check that the Host excludes child business implementations and ordinary contracts/business packages do not depend on RPC.

Available runnable examples are listed in the [example index](../examples/README.md). Program configuration rules are documented in [Concepts](concepts.md#declare-program-configuration). Real process checks require Unix sockets and local listeners; write verification output to a temporary directory.

## Release boundaries

This is an independent module in the Gordis repository at `rpc/`, with module path `github.com/RussellLuo/gordis/rpc`. Core retains `github.com/RussellLuo/gordis` and does not depend on RPC. Publish RPC versions with tags such as `rpc/v0.1.0`; Core uses root tags such as `v0.1.0`. The RPC module is not published yet and its go.mod has no local replaces. jsonschema-go uses its pinned remote version.

The pinned Core version does not yet include the new events cooperation APIs. Before independent release, pin go.mod to a real Core version containing them and rerun complete checks with `GOWORK=off`; workspace verification does not establish release dependency compatibility.

Local verification has covered darwin/arm64. Normal version builds, standalone release dependencies and other platforms need separate verification. Update Host, child and generated code together when changing unpublished APIs or protocols.
