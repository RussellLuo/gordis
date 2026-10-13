# Optional service discovery and fallback

English | [中文](README.zh.md)

One reader uses a required database and probes an optional cache on each request.
The program mounts and removes the cache while the same reader stays Ready.
Both stores use in-memory values; no external services are needed.

## Run

From the repository root:

```sh
go run ./examples/optional-service
```

Expected output:

```text
reader generation 1: database:item
reader generation 1: cache:item
reader generation 1: database:item
```

## How it works

The reader declares `databaseKey` in `Requires` and obtains the database through
`Get` during activation. `Activate` also injects `lookupCache`, a function that
calls `scope.Lookup(cacheKey)` on each request.

`readerCh` transfers the initialized reader to the program. Its buffer lets
`Activate` send while the program waits for `MountReady`, which guarantees
readiness before requests begin. The generation number is only for output.

Each request uses the cache when found and the database otherwise. Mounting the
cache switches the next request to it; unmounting switches back to the database.
Lookup creates no dependency edge, so the reader stays in the same generation.

Requests run synchronously, and Shutdown follows the final request. Lookup does
not guarantee that the cache survives a subsequent call. The example falls back
only on absence; fallback after a failed cache call is a business decision.
For managing concurrent requests during shutdown, see
[lifecycle](../lifecycle/README.md).

See [main.go](main.go) for the complete program and
[Writing Plugins](../../docs/plugin-authoring.md#probe-an-optional-service) for
the full contract. The [dynamic](../dynamic/README.md) example demonstrates
provider changes with a required dependency, while
[readiness](../readiness/README.md) demonstrates a conditional child.
