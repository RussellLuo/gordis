# Bidirectional events

English | [中文](README.zh.md)

One round trip uses ordinary typed Topics in both directions:

```text
Host Emit(Request) → Child On(Request)
                        → Publish(Notice) → Host On(Notice) → print
Host waits for Request to return → Shutdown
```

This replaces the old `examples/process-events` example, adding a Host request
to its Child-to-Host Notice. Business plugins use ordinary Gordis events APIs.

## Run

From `rpc/`:

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go build -o /tmp/gordis-rpc-events-host ./examples/events/host/cmd
go build -o /tmp/gordis-rpc-events-plugin ./examples/events/plugin/cmd
/tmp/gordis-rpc-events-host /tmp/gordis-rpc-events-plugin
```

These commands use a temporary workspace for this checkout's Core and RPC
modules. The pinned release Core version does not yet contain the remote events
cooperation APIs; update it to a real version before standalone release builds.
No local replace or permanent workspace is required.

The only business output is:

```text
received notice: hello
```

The CLI accepts just the plugin executable path. RPC diagnostics go to stderr.
Mounting and waiting for Request have a bounded context; deferred Shutdown has
its own timeout and propagates cleanup errors.

## Ordinary code and assembly

[contract](contract/notice.go) defines Request, Notice and their typed Topics.
The [Child plugin](plugin/plugin.go) subscribes to Request and publishes Notice
from its handler. The [Host plugin](host/plugin.go) prints Notice directly in its
handler and calls Emit from an AfterReady task.
These packages depend on Gordis/events and the contract, with no RPC runtime
or bindings dependency. The Host does not import the Child implementation.

[bindings](bindings/notice.go) constructs the same Bridge on both sides.
Directions are relative to the Child: Request permits Subscribe; Notice permits
Publish. Each Topic has an explicit stable JSON codec ID. These are handwritten
event declarations, so this example has no rpcgen step.

The [Host entry point](host/cmd/main.go) mounts the Bus, Bridge process plugin,
then observer. The Child's Request subscription is installed before the observer
starts. AfterReady ensures the observer can send events and receive Notice.
One buffered channel reports the Emit result so the CLI waits for the request
task before shutting down. MountReady alone does not wait for AfterReady tasks.
Request uses ordered Emit, allowing the nested Notice callback without requiring
extra parallel Bus capacity.

The [Child entry point](plugin/cmd/main.go) applies Bridge.Unit to one ordinary
plugin instance, then calls ServeUnit. There are no ordinary Service exports,
imports or program configuration. The bridge supplies the real private
endpoint and remote Bus; the Child does not install a local events.Plugin.

The printed line means Notice was received. Any later Request error still makes
the CLI fail. Transport limits and failure scenarios are covered by library tests.

## Verification

From `rpc/`, in the same shell with the workspace configured above:

```sh
go test -race -count=1 -timeout=180s ./examples/...
go test -race -run '^TestBridgeNestedNotificationCompletion$' -count=10 -timeout=180s ./events
go vet ./examples/...
```

Tests build both real programs with `-race`, require the exact single output
and successful exit, and audit production dependencies. The dedicated
TestBridgeNestedNotificationCompletion library test blocks a Notice handler
after receipt, verifies that the original Emit waits, and checks error propagation
after release. Hook, routing, cancellation and actual cleanup matrices also live
in the [events library tests](../../events/README.md).
