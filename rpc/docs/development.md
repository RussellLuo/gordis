# Development and verification

English | [中文](development.zh.md) · [README](../README.md)

Run these commands from `rpc/` with a Go toolchain matching [go.mod](../go.mod). `GOWORK=off` uses the module's own dependencies. Set `GOCACHE=/tmp/qi-review-cache` if you need a writable cache.

The module pins `github.com/RussellLuo/jsonschema-go v0.0.0-20261020143036-d0e0ff947dfa` with jsonschemax support. Go downloads it through normal module resolution; no adjacent jsonschema-go checkout or local replace is needed. Qi development workspaces and integration probes use the same pinned dependency.

## Common checks

Core and RPC are separate modules: a root `go test ./...` does not test `rpc/`. Run Core checks from the Gordis root and the commands below from `rpc/`, both with `GOWORK=off`. This checks RPC against the Core version declared in its own go.mod rather than a workspace override.

```sh
# Runtime, generator and real child-process regressions.
GOWORK=off go test -race -count=1 -timeout=180s ./...

# Only public examples: real Host/child and Local/process comparisons.
GOWORK=off go test -race -count=1 ./examples/...

# Regenerate library fixtures after generator or contract changes.
GOWORK=off go generate ./internal/genfixture/contract ./internal/streamfixture/contract
```

After generation, check consistency without writing:

```sh
cd internal/genfixture/contract
GOWORK=off go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go -check
```

See [Bindings](bindings.md) and [basic](../examples/basic/README.md) for basic generation and run commands. Commit generated files; consumers should not need generation during builds.

## What each check establishes

Module tests cover configuration declaration, Schema adaptation, defaults, strict decoding, mapping and isolation; DescribeProgram reaping without initialization; Host validation before Initialize and independent child validation; Unit/boundary rejection, handshake checks, startup deadlines, export validation cancellation, real subprocess calls and callbacks, stream cancellation, activation rollback and reaping. They test the adapter's responsibilities, not a second copy of jsonschema-go's inference/constraint matrix or full meta-schema validity. Fixtures live in the module and need neither Qi nor the PoC. Example tests also check that the Host excludes child business implementations and ordinary contracts/business packages do not depend on RPC.

To check the Qi tool adapter and Profile integration, run from the Qi repository root with its workspace enabled:

```sh
go test -race -count=1 -timeout=180s ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/...
go test -race ./kernel/boot/...
```

These checks cover tool semantics, process cleanup, default registration and Profile path preparation. They do not establish external real-model behavior. Available runnable examples are listed in the [example index](../examples/README.md).

Program configuration rules are documented in [Concepts](concepts.md#declare-program-configuration). The commands above cover configuration, runtime/protocol and Qi integration. Real process checks require Unix sockets and local listeners; write verification output to a temporary directory.

## Release boundaries

This is an independent module in the Gordis repository at `rpc/`, with module path `github.com/RussellLuo/gordis/rpc`. Core retains `github.com/RussellLuo/gordis` and does not depend on RPC. Publish RPC versions with tags such as `rpc/v0.1.0`; Core uses root tags such as `v0.1.0`. The RPC module is not published yet and its go.mod has no local replaces. Qi development maps this module to the adjacent Gordis checkout (`../gordis/rpc`); qi-build receives that source mapping explicitly. jsonschema-go uses its pinned remote version.

Local verification has covered darwin/arm64. Normal version builds, standalone release dependencies, other platforms and external real-model flows need separate verification. This library does not replace Qi's LLM/prompt adapters. Update Host, child and generated code together when changing unpublished APIs or protocols.

Protocol remains `gordis-rpc/1` with transport handshake version 1; this does not allow mixing binaries built before and after the configuration payload change. ConfigTarget, ConfigMode, ProcessConfigMode, ForwardConfig and NoConfig have been removed. Declare configured programs with Unit.Config/NewConfig/Configure and unconfigured programs with ConfigSpec's zero value. The [stream example](../examples/stream/README.md) is available; the Unit recipe remains a later stage.
