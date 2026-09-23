# Optional child failure

This example starts a healthy `collector` with an optional `metrics` child.
The child fails during startup and releases its resource, while the parent
remains ready and usable.

## Run

From the repository root:

```sh
go run ./examples/optional
```

Expected output:

```text
metrics: resource released
Start: gordis: start metrics generation 1: metrics backend unavailable
collector: state=ready, optional=false, groupReady=true, cleanupComplete=false
metrics: state=failed, optional=true, groupReady=false, cleanupComplete=true
collector: main functionality remains available
```

## How it works

`Optional: true` excludes the child branch from its parent's `GroupReady`
result. It does not hide the startup error: `Host.Start` still reports the
failure, and the application decides whether the healthy group can continue.

The failed child's registered cleanup runs before startup returns. The
snapshots show that `metrics` is failed and fully cleaned up, while `collector`
remains ready with `GroupReady: true`.

See [main.go](main.go) for the complete program.
