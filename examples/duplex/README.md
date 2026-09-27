# Bidirectional process calls

English | [中文](README.zh.md)

This example demonstrates one nested call over a single stdio session. The
Host calls the plugin's `add(2)` method. While handling that request, the plugin
calls the Host's `base()` method, receives `40`, and returns `42`.

It uses the process protocol directly, without a UI, HTTP, or a Gordis service
graph.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-duplex-host ./examples/duplex
go build -o /tmp/gordis-duplex-plugin ./examples/duplex/plugin
/tmp/gordis-duplex-host /tmp/gordis-duplex-plugin
```

Expected output:

```text
Host → plugin: add(2)
plugin → Host: base()
result: 42; plugin process reaped
```

## How it works

The Host supplies a callback handler in `process.Options` and calls the plugin
through `client.Call`. The plugin handles that request through
`process.Handler.Call` and uses the provided `process.Caller` to call back into
the Host. Both sides share method names and identity metadata from the contract
package. Protocol messages use stdout; plugin logs should use stderr.

Key files:

- [main.go](main.go): starts the process and handles Host callbacks.
- [plugin/main.go](plugin/main.go): handles plugin calls and calls the Host.
- [contract/contract.go](contract/contract.go): shared protocol identifiers.

See [process](../process/README.md) for a typed Gordis service backed by a child
process and [process protocol](../../docs/process-protocol.md) for protocol
details.
