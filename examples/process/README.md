# Process plugin

English | [中文](README.zh.md)

This example runs a regular Local-first Greeter Plugin in a child process. The
optional process boundary lives in `greeter/proc`; the Plugin ID, configuration,
typed `greeter.Key`, and consumer remain the same as in local mode.

## Run

From the repository root:

```sh
go build -o /tmp/gordis-process-host ./examples/process
go build -o /tmp/gordis-process-plugin ./examples/process/cmd/greeter-plugin
/tmp/gordis-process-host /tmp/gordis-process-plugin
```

Expected output:

```text
Hello, Gordis!
```

## How it works

`greeter/plugin.go` defines an ordinary business Plugin with no process-specific
dependencies. The Host registers the process-backed prototype returned by
`greeterproc.New`; mounting it with `MountReady` starts the child executable and
forwards `InstanceSpec.Config` to the same Plugin through `greeterproc.Serve`.

The explicit binding in `greeter/proc` exposes the remote Greeter as the typed
`greeter.Key`, so the consumer uses the normal `gordis.Get` API without knowing
where the provider runs. Registering `new(greeter.Plugin)` instead switches to
Local mode without changing the `InstanceSpec`, configuration, or consumer.
When `run` returns, `Host.Shutdown` stops and reaps the child process.

See [duplex](../duplex/README.md) for an external Plugin that both consumes and
provides typed Services, and [application-plugin](../application-plugin/README.md)
for a backend and UI delivered together.
