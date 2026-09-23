# Process plugin

This example starts an empty Host, attaches an independently built Greeter
plugin with `Preview` and `Apply`, calls its typed service, and reaps the child
process during shutdown.

The Host imports only the shared contract and wire adapter. The Greeter
implementation is not linked into the Host binary.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-process-host ./examples/process
go build -o /tmp/gordis-process-plugin ./examples/process/plugin
/tmp/gordis-process-host /tmp/gordis-process-plugin
```

Expected output, with different PIDs on each run:

```text
host PID <host-pid> started without a plugin
plugin PID <plugin-pid>: Hello, Gordis!
plugin process reaped
```

## How it works

The Host registers a generic `processbridge.Adapter` under the logical plugin
ID `greeter`. When the dynamic change is applied, the adapter starts the child
process and exposes its RPC binding as the typed `Greeter` service. Consumers
depend only on `GreeterKey`, so they do not need to know whether the provider is
local or runs in another process.

Key files:

- [main.go](main.go): Host assembly and late plugin attachment.
- [plugin/main.go](plugin/main.go): Greeter plugin process.
- [contract/contract.go](contract/contract.go): shared typed contract.
- [contract/wire/wire.go](contract/wire/wire.go): RPC binding.

See [duplex](../duplex/README.md) for bidirectional process calls and
[application-plugin](../application-plugin/README.md) for a backend and UI
delivered together.
