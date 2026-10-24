# gordis-rpc

[English](README.md) | [中文](README.zh.md)

Run ordinary [Gordis](https://github.com/RussellLuo/gordis) Plugins in trusted local child processes. Consumers keep using typed service interfaces through `scope.Get`; gordis-rpc handles process startup, RPC calls and cleanup.

An independent Go module: `github.com/RussellLuo/gordis/rpc` (package `rpc`). It uses HashiCorp go-plugin/gRPC and has no Qi dependency. It lives in the Gordis repository under `rpc/`, has no published release, and its API and wire protocol may change. The Core module does not depend on RPC.

## Try it

From `rpc/`:

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
# Hello, Gordis!
```

The [basic walkthrough](examples/basic/README.md) explains configuration, the ordinary Plugin, generated Binding and plugin program Unit. The Host program uses `NewProcess` to register the remote provider; the plugin program uses `ServeUnit` to run its assembly.

Ordinary unary and stream calls share the per-Session Business budget. Admission fields are unlimited when zero, positive values set explicit caps, and negative values are invalid. Set capacities explicitly in `rpc.Limits` for your workload; the library supplies no numeric admission preset. Excess calls under a finite cap fail immediately without a wait queue. Use `errors.Is(err, rpc.ErrBackpressure)` to identify resource exhaustion, including message-size limits, and inspect the error for its cause. Host and child configure their own `Limits`; Host settings do not change the child's limits. See [immediate admission](docs/bindings.md#immediate-admission).

## Read more

- [Core concepts](docs/concepts.md): contract, Binding, Boundary, Unit, Session and program configuration.
- [Architecture and lifecycle](docs/architecture.md): responsibilities, startup, callbacks and cleanup.
- [Bindings](docs/bindings.md): generate an adapter from a Go interface, or write a Codec.
- [Development and verification](docs/development.md): commands and release boundaries.
- [Examples](examples/README.md): available runnable examples.
- [RPC events](events/README.md): typed Topic notifications, Hook selection/Waterfall, source routing, subscription ownership and Ready coordination across processes.
- [Bidirectional events example](examples/events/README.md): Host Request, Child Notice, and ordinary event handlers.
