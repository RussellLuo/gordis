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
	ctx := context.Background()
	host, err := gordis.NewHost([]gordis.Plugin{new(greeterPlugin)})
	if err != nil {
		log.Fatal(err)
	}
	defer host.Shutdown(context.Background())

	instance, err := host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "greeter", Plugin: "greeter",
	})
	if err != nil {
		log.Fatal(err)
	}
	if err = instance.Unmount(ctx); err != nil {
		log.Fatal(err)
	}
}
```

Running the program prints:

```text
greeter: Hello, Gordis!
```

`NewHost` fixes the compiled Plugin type catalog. `Env.MountReady` adds one
root instance and waits for its exact desired revision; the returned `Instance`
can later be updated, restarted, awaited, or unmounted.

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

Loaders and other control planes use `Host.Changes()` to build cross-instance
changes and `ChangeSet.Apply` to validate and atomically commit the complete
candidate. The returned Operation observes commit, convergence completion, and
readiness of its explicit targets separately. The core does not read
configuration files or maintain a second Loader runtime graph.

`processbridge` can provide the same service contract from a child process
without changing consumers.

The optional `events` package provides Scope-owned typed Topics/Hooks with
local and Process delivery, stable ordering, bounded parallelism, selection,
and waterfall dispatch.

## Examples

Start with [basic](examples/basic/README.md) for typed services, configuration,
and instance mounting. See the [examples guide](examples/README.md) for
events, isolation, ownership, readiness, dynamic changes, process plugins, and
application integration.

## Documentation

- [Core Concepts](docs/concepts.md): objects, service bindings, ownership, and
  dependencies.
- [Writing Plugins](docs/plugin-authoring.md): contracts, cleanup, and process
  adaptation.
- [Lifecycle](docs/lifecycle.md): startup, draining, failure propagation, and
  diagnostics.
- [Dynamic Instance Management](docs/dynamic.md): runtime changes, waiting, and
  restore.
- [Process Protocol](docs/process-protocol.md): bidirectional RPC and remote
  cleanup.

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
