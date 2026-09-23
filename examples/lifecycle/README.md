# Lifecycle-owned resources

This example gives one Scope a managed background task, a stop callback, an
in-flight request lease, and final resource cleanup. It shows how shutdown
closes admission before waiting for active work.

## Run

From the repository root:

```sh
go run ./examples/lifecycle
```

Expected output:

```text
entrance closed
task stopped
new request rejected
stop waits for the request
request finished
resource released
stop complete
```

## How it works

- `OnStop` closes the public entrance.
- `Go` owns a task that exits when the Scope context is canceled.
- `Acquire` holds a lease for one admitted request and rejects new requests
  after shutdown begins.
- `Defer` releases the final resource after tasks and leases have drained.

The example holds a lease while calling `Host.Stop`, verifies that new work is
rejected, and then releases the request so shutdown can finish.

See [main.go](main.go) for the complete program and
[Lifecycle](../../docs/lifecycle.md) for the full shutdown sequence.
