# Events from a process plugin

This example mounts an independently built process plugin that publishes a
typed Topic into the Host EventBus. A Local plugin receives the event through
the same `events.Bind(scope).On` API used for Local publishers.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-process-events-host ./examples/process-events
go build -o /tmp/gordis-process-events-plugin ./examples/process-events/plugin
/tmp/gordis-process-events-host /tmp/gordis-process-events-plugin
```

Expected output:

```text
received from process: hello
plugin process reaped
```

## How it works

The Host first mounts `events.Plugin` and a Local observer, then mounts the
`processbridge.Adapter` for `remote-publisher`. The remote plugin uses
`Scope.AfterReady` to publish only after its generation and the event bridge
are Ready.

Both process endpoints register `wire.Notice`. It binds the shared typed Topic
to a JSON codec with a stable wire ID and allows `EventPublish`, meaning the
process may publish into the Host Bus. The process handshake rejects a missing
or mismatched event binding before the remote plugin activates.

The Host observer needs no process-specific code. The bridge routes the remote
publication through the Host EventBus, so the ordinary Topic routing and Scope
lifecycle rules still apply. Host shutdown then stops and fully reaps the child
process.

Key files:

- [main.go](main.go): Host assembly and Local subscriber.
- [plugin/main.go](plugin/main.go): remote publisher.
- [contract/contract.go](contract/contract.go): shared typed Topic and payload.
- [contract/wire/wire.go](contract/wire/wire.go): explicit process event wire.

See [events](../events/README.md) for Local publication,
[process](../process/README.md) for a typed Service backed by a process, and the
[process protocol](../../docs/process-protocol.md#event-federation) for the full
federation contract.
