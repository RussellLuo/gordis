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

## Project components

| Component | Purpose |
| --- | --- |
| Core | The `github.com/RussellLuo/gordis` module provides plugin assembly, typed services, and lifecycle management. |
| [Events](examples/events/README.md) | The `events` subpackage within Core adds event communication between plugins. |
| [RPC](rpc/README.md) | The independent `github.com/RussellLuo/gordis/rpc` module runs plugins in trusted local child processes, supporting typed services and events. |
| [Build](build/README.md) | The independent `github.com/RussellLuo/gordis/build` module provides a build SDK and native `gordis-build` command; [minimal](build/examples/minimal/README.md) uses the command, while [custom](build/examples/custom/README.md) preserves application registration descriptions through the SDK. |

HTTP, UI, agents, package distribution, and persistence belong to the application.

## Continue learning

1. [Basic example](examples/basic/README.md): share a typed service between
   plugins and configure individual instances.
2. [Core Concepts](docs/concepts.md): understand objects, ownership, service
   dependencies, and visibility.
3. [Writing Plugins](docs/plugin-authoring.md): define contracts, manage
   resources, and assemble an application.

Consult [Lifecycle](docs/lifecycle.md) and
[Dynamic Instance Management](docs/dynamic.md) as needed. The
[examples guide](examples/README.md) covers isolation, ownership, readiness,
dynamic changes, and optional extensions.

## Acknowledgements

Gordis is inspired by [Cordis](https://github.com/cordiverse/cordis), especially
its ideas around service composition, dependency-aware plugin lifecycles, and
lifecycle-owned resource cleanup. Gordis reinterprets these ideas for Go and is
an independent project, not a Go port or API-compatible implementation of
Cordis.

Thanks to Shigma and the Cordis contributors for the ideas and work that
informed Gordis.
