# Gordis RPC examples

English | [中文](README.zh.md)

Each example focuses on one part of running ordinary Gordis Plugins in a child
process. Start with `basic`, then continue with `duplex` and `stream`.
The linked README in each directory contains run instructions, expected output,
and an explanation of the implementation.

## Process communication

| Example | Focus |
| --- | --- |
| [basic](basic/README.md) | A typed remote Service, program configuration, and Local/process comparison |
| [duplex](duplex/README.md) | Imported Host services and nested callbacks |
| [stream](stream/README.md) | Typed streaming results, complete consumption, and early stopping |

## Verification

From `rpc/`, run:

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

The tests build and run real Host and plugin programs, and check configuration,
Local/process behavior, streaming results, and dependency boundaries.
