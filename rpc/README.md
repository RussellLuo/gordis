# gordis-rpc

English | [中文](README.zh.md)

Run ordinary [Gordis](../README.md) Plugins in trusted local child processes. Consumers keep using typed service interfaces through `scope.Get`; gordis-rpc handles process startup, RPC calls and cleanup. Process isolation is not a sandbox for untrusted code.

## Try it

From `rpc/`:

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
# Hello, Gordis!
```

The Host uses `NewProcess` to connect to the child; the child uses `ServeUnit` to run its plugin assembly.

To run `events` or full source checks, use a [temporary workspace](docs/development.md#common-checks) with this repository's Core source; the pinned Core version lacks the events cooperation APIs.

## Read more

1. [Basic walkthrough](examples/basic/README.md): follow the contract, business plugins, configuration and program assembly.
2. [Core concepts](docs/concepts.md): understand service boundaries and child assembly.
3. [Bindings](docs/bindings.md): generate RPC adapters from Go interfaces and configure limits and error handling.

Consult [Architecture and lifecycle](docs/architecture.md) for startup, callbacks and cleanup, and [Development and verification](docs/development.md) for checks and release requirements.

Explore more runnable scenarios in [Examples](examples/README.md). For cross-process event communication, see [RPC events](events/README.md).
