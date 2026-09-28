# Bidirectional process services

English | [中文](README.zh.md)

This example shows an external Plugin both consuming and providing typed
Services. A consumer in the Host calls the external Greeter's
`Greet("user-42")`. While handling that request, Greeter uses `Requires` to call
the Host's `UserDirectory.DisplayName("user-42")` and returns `Hello, Gordis!`.

The business Plugin still uses ordinary `Spec`, `Activate`, `Get`, and `Provide`;
explicit `processbridge` bindings carry both directions across the process
boundary.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-duplex-host ./examples/duplex
go build -o /tmp/gordis-duplex-plugin ./examples/duplex/cmd/greeter-plugin
/tmp/gordis-duplex-host /tmp/gordis-duplex-plugin
```

Expected output:

```text
Host consumer → process Greeter: greet("user-42")
process Greeter → Host UserDirectory: displayName("user-42")
result: Hello, Gordis!
```

## How it works

`greeter.Plugin` declares `Requires: UserDirectoryKey` and `Provides: GreeterKey`.
The Host Adapter installs a Host handler for `UserDirectoryKey` and publishes
the remote Greeter as a typed proxy under the same `GreeterKey`. In the child,
`Serve` injects a client proxy for `UserDirectoryKey` and installs a handler for
the real Greeter.

The call therefore stays within the ordinary Service model:

```text
Host consumer → remote Greeter → Host UserDirectory → remote Greeter → Host consumer
```

Registering `new(greeter.Plugin)` instead of the process-backed prototype
switches to Local mode without changing Greeter, the UserDirectory provider, or
the consumer business code.

Key files:

- [main.go](main.go) assembles the Host UserDirectory, external Greeter, and local consumer.
- [greeter/plugin.go](greeter/plugin.go) is an ordinary process-independent business Plugin.
- [greeter/proc/bridge.go](greeter/proc/bridge.go) contains the bidirectional `UserDirectory` and `Greeter` bindings.
- [cmd/greeter-plugin/main.go](cmd/greeter-plugin/main.go) is the child-process entrypoint.

See [process](../process/README.md) for the simpler one-way Process Service.
See [the process protocol](../../docs/process-protocol.md) for lower-level stdio
RPC and nested-call constraints.
