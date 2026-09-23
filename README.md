# Gordis

English | [中文](README.zh.md)

Gordis is a lightweight framework for composing Go plugins through typed
services and managing instance startup, shutdown, and resource cleanup in
dependency order.

## Quick start

A plugin describes its type with `Spec` and starts its work in `Start`:

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

func (*greeterPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("%s: Hello, Gordis!\n", scope.ID())
	return nil
}

func main() {
	ctx := context.Background()
	host, err := gordis.NewHost(
		[]gordis.Plugin{new(greeterPlugin)},
		[]gordis.InstanceSpec{{ID: "greeter", Plugin: "greeter"}},
	)
	if err != nil {
		log.Fatal(err)
	}
	if err = host.Start(ctx); err != nil {
		log.Fatal(err)
	}
	if err = host.Stop(ctx); err != nil {
		log.Fatal(err)
	}
}
```

Running the program prints:

```text
greeter: Hello, Gordis!
```

`InstanceSpec` adds one `greeter` instance; the Host validates, starts, and
stops it. The [basic example](examples/basic/README.md) continues with a typed
service, configuration, and multiple instances.

## Core model

```text
Plugin prototype ──Spec──> PluginSpec
InstanceSpec ──selects──> PluginSpec.ID

PluginSpec + InstanceSpec ──> Host
Host ──activates──> instance generation
                    ├── runtime Plugin
                    └── Scope

Parent = lifecycle ownership
Key[T] (Requires/Provides) + slots = service dependency and binding
```

`PluginSpec` describes a reusable plugin type; `InstanceSpec` selects it and
configures a concrete instance. The Host validates the resulting graph. Each
activation creates a fresh runtime Plugin and Scope, which owns its tasks,
requests, and cleanup.

`Parent` expresses lifecycle ownership. `Key[T]`, `Requires`/`Provides`, and
slots express service dependencies and bindings. Together they determine
startup and cleanup order.

`processbridge` can provide the same service contract from a child process
without changing consumers.

## Examples

The examples form a gradual learning path:

| Example | Focus |
| --- | --- |
| [basic](examples/basic/README.md) | Typed services and per-instance configuration |
| [composition](examples/composition/README.md) | Multiple service slots and parent/child ownership |
| [optional](examples/optional/README.md) | Optional child failure and `GroupReady` |
| [lifecycle](examples/lifecycle/README.md) | Managed tasks, request leases, and cleanup order |
| [dynamic](examples/dynamic/README.md) | `Preview`, `Apply`, pending state, and `Restore` |
| [process](examples/process/README.md) | A typed service provided by an independently built process |
| [duplex](examples/duplex/README.md) | Bidirectional Host/plugin calls over stdio |
| [application-plugin](examples/application-plugin/README.md) | A versioned backend and UI delivered after Host startup |

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
