# Gordis

English | [中文](README.zh.md)

Gordis is a lightweight framework for composing Go plugins through typed
services and managing instance startup, shutdown, and resource cleanup in
dependency order.

## Quick start

A plugin describes its type with `Spec` and performs startup work in `Activate`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/RussellLuo/gordis"
)

type greeterPlugin struct{}

func (*greeterPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "greeter",
		New: func() gordis.Plugin { return new(greeterPlugin) },
	}
}

func (*greeterPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("%s: Hello, Gordis!\n", scope.ID())
	return nil
}

func main() {
	host, err := gordis.NewHost(new(greeterPlugin))
	if err != nil {
		log.Fatal(err)
	}
	defer host.Shutdown(context.Background())

	if _, err := host.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "greeter", Plugin: "greeter",
	}); err != nil {
		log.Fatal(err)
	}
}
```

Running the program prints:

```text
greeter: Hello, Gordis!
```

`NewHost` registers the available plugin types. `Env.MountReady` mounts an
instance and waits for it to become ready. `Host.Shutdown` cleans up mounted
instances when the program exits.

## Core model

- **Plugin**: a reusable type whose `PluginSpec` declares its identity,
  constructor, and service dependencies.
- **Instance**: one use of a plugin type, with its own ID and configuration
  selected by `InstanceSpec`. The Host coordinates startup and shutdown in
  dependency order.
- **Service**: a typed Go value shared through `Key[T]`. Providers declare
  `Provides` and publish values with `scope.Provide`; consumers declare
  `Requires` and access them with `scope.Get`.
- **Scope**: owns one activation's tasks, requests, and cleanup. Each activation
  gets a fresh plugin object and Scope.

## Examples

Start with [basic](examples/basic/README.md) for typed services, configuration,
and instance mounting. See the [examples guide](examples/README.md) for
events, isolation, ownership, readiness, dynamic changes, and process plugins.

## Optional extensions

- **Events**: the `github.com/RussellLuo/gordis/events` package adds event
  communication between plugins. See the [events example](examples/events/README.md).
- **RPC**: the [`github.com/RussellLuo/gordis/rpc` module](rpc/README.md) runs
  plugins in trusted local child processes. Add it when you need process
  execution; consumers keep using typed services. Follow the
  [RPC examples](rpc/examples/README.md): `basic`, `duplex`, `stream`, then `events`.

## Documentation

- [Core Concepts](docs/concepts.md): start here for the complete relationship
  among objects, ownership, Service dependencies, and visibility.
- [Writing Plugins](docs/plugin-authoring.md): contracts, resource management,
  and application assembly.
- [Lifecycle](docs/lifecycle.md): reference for startup, draining, failure
  propagation, and diagnostics.
- [Dynamic Instance Management](docs/dynamic.md): single-instance operations,
  bulk changes, waiting, and recovery.
- [RPC Architecture and Lifecycle](rpc/docs/architecture.md): process startup,
  service calls, callbacks, and cleanup.
- [Legacy stdio Process Protocol](docs/process-protocol.md): historical reference
  for the removed implementation and its migration index.

## Scope

Gordis core manages the Go plugin graph, typed services, and lifecycle. HTTP,
UI, agents, package distribution, and persistence are application concerns.

Use the RPC module for ordinary Services and its [events bridge](rpc/events/README.md)
for events in trusted local child processes. The legacy `process`,
`processbridge`, and application-plugin example, including its package loader,
have been removed. Standalone release dependency validation remains pending.
Migrating from the old stdio protocol requires rebuilding Host and child programs;
RPC supplies no manifest/package manager.

Process execution does not provide a sandbox for untrusted code. RPC does not
provide remote discovery, authentication, reconnection, or forced goroutine termination.

## Acknowledgements

Gordis is inspired by [Cordis](https://github.com/cordiverse/cordis), especially
its ideas around service composition, dependency-aware plugin lifecycles, and
lifecycle-owned resource cleanup. Gordis reinterprets these ideas for Go and is
an independent project, not a Go port or API-compatible implementation of
Cordis.

Thanks to Shigma and the Cordis contributors for the ideas and work that
informed Gordis.
