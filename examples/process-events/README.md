# Events from a process plugin

English | [中文](README.zh.md)

This example runs a regular Local-first Notice publisher Plugin in a child
process. The optional process boundary lives in `notice/proc`; the Plugin ID,
typed Topic, publisher, and Local observer remain the same as in local mode.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-process-events-host ./examples/process-events
go build -o /tmp/gordis-process-events-plugin ./examples/process-events/cmd/notice-publisher
/tmp/gordis-process-events-host /tmp/gordis-process-events-plugin
```

Expected output:

```text
received notice: hello
```

## How it works

`notice/plugin.go` defines an ordinary publisher Plugin and its typed Topic. The
Host mounts `events.Plugin`, a Local observer, and the process-backed prototype
returned by `noticeproc.New`. Mounting the publisher starts the child executable,
where `noticeproc.Serve` runs the same Plugin. Its `Scope.AfterReady` callback
publishes only after the generation and event bridge are Ready.

Both process endpoints use the binding in `notice/proc`. It binds the shared
typed Topic to a JSON codec with a stable wire ID and allows `EventPublish`,
meaning the child may publish into the Host EventBus. The process handshake
rejects a missing or mismatched event binding before the remote Plugin
activates.

The Host observer needs no process-specific code: it subscribes through the
normal `events.Bind(scope).On` API. Registering `new(notice.Plugin)` instead
switches the publisher to Local mode without changing the `InstanceSpec`, Topic,
publisher, or observer. When `run` returns, `Host.Shutdown` stops and reaps the
child process.

See [events](../events/README.md) for Local publication,
[process](../process/README.md) for a typed Service backed by a process, and the
[process protocol](../../docs/process-protocol.md#event-federation) for the full
federation contract.
