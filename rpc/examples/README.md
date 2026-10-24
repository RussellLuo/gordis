# Gordis RPC examples

English | [中文](README.zh.md)

Each example focuses on one part of running ordinary Gordis Plugins in a child
process. Start with `basic`, then continue with `duplex`, `stream` and `events`.
The linked README in each directory contains run instructions, expected output,
and an explanation of the implementation.

Keep each runnable example focused on its core business flow. Concurrency,
failure propagation and cleanup guarantees belong in dedicated library tests;
examples retain the timeouts and shutdown needed to run correctly.

## Process communication

| Example | Focus |
| --- | --- |
| [basic](basic/README.md) | A typed remote Service, program configuration, and Local/process comparison |
| [duplex](duplex/README.md) | Imported Host services and nested callbacks |
| [stream](stream/README.md) | Typed streaming results, complete consumption, and early stopping |
| [events](events/README.md) | Host Request and Child Notice through a bidirectional event bridge |

## Verification

From `rpc/`, run:

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go test -race -count=1 -timeout=180s ./examples/...
```

The tests build and run real Host and plugin programs, and check configuration,
Local/process behavior, streaming results, event round-trip completion, and
dependency boundaries. The temporary workspace maps the current Core and RPC modules; standalone publication still requires updating
the pinned Core dependency for the new events cooperation APIs.
