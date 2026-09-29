# Gordis

English | [中文](README.zh.md)

Gordis is a lightweight framework for composing Go plugins through typed
services and managing instance startup, shutdown, and resource cleanup in
dependency order.

## Quick start

A plugin describes its type with `Spec` and starts one generation in
`Activate`:

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
	host, err := gordis.NewHost([]gordis.Plugin{new(greeterPlugin)})
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

`NewHost` fixes the compiled Plugin type catalog. `Env.MountReady` adds one
root instance and waits for its exact desired revision. `Host.Shutdown` cleans
up mounted instances when the program exits.

## Core model

```text
Plugin prototype ──Spec──> PluginSpec
NewHost(Plugin types) ──> Host ──> synthetic root Env
root Env + InstanceSpec ──Mount──> logical Instance
                                      └── generation
                                          ├── runtime Plugin
                                          └── Scope

Key[T] (Requires/Provides) + Env View = fixed service binding
```

`PluginSpec` describes a reusable plugin type; `InstanceSpec` selects it and
configures a concrete root instance. Each activation creates a fresh runtime
Plugin and Scope, which owns its tasks, requests, and cleanup. Missing required
services put the logical Instance in `pending`; mounting the provider lets the
Host converge it automatically.

`Env.Isolate` derives an immutable visibility View. `Key[T]` and
`Requires`/`Provides` determine dependency and cleanup order, while the View
selects the provider label fixed for one generation.

Ownership and readiness are deliberately separate: a child follows its owner
generation for restart and cleanup, but only declared Service dependencies
decide whether an instance can become ready.

Control planes can use `Host.Changes()` to atomically commit cross-instance
changes. The optional `events` and `processbridge` packages follow the same
Service, Scope, and lifecycle model; the core does not read configuration files
or maintain a second Loader graph.

## Examples

Start with [basic](examples/basic/README.md) for typed services, configuration,
and instance mounting. See the [examples guide](examples/README.md) for
events, isolation, ownership, readiness, dynamic changes, process plugins, and
application integration.

## Documentation

- [Core Concepts](docs/concepts.md): start here for the complete relationship
  among objects, ownership, Service dependencies, and visibility.
- [Writing Plugins](docs/plugin-authoring.md): contracts, resource management,
  and application assembly.
- [Lifecycle](docs/lifecycle.md): reference for startup, draining, failure
  propagation, and diagnostics.
- [Dynamic Instance Management](docs/dynamic.md): single-instance operations,
  bulk changes, waiting, and recovery.
- [Process Protocol](docs/process-protocol.md): advanced, read-as-needed
  reference for bidirectional RPC and remote cleanup.

## Scope

Gordis core manages the Go plugin graph, typed services, and lifecycle. HTTP,
UI, agents, package distribution, and persistence are application concerns.

The process transport runs trusted local stdio children. It does not provide
remote discovery, authentication, reconnection, in-place configuration
mutation, overlapping old/new generations, or forced goroutine termination.

## Acknowledgements

Gordis is inspired by [Cordis](https://github.com/cordiverse/cordis), especially
its ideas around service composition, dependency-aware plugin lifecycles, and
lifecycle-owned resource cleanup. Gordis reinterprets these ideas for Go and is
an independent project, not a Go port or API-compatible implementation of
Cordis.

Thanks to Shigma and the Cordis contributors for the ideas and work that
informed Gordis.
