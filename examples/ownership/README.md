# Nested lifecycle ownership

This example mounts a child from an owner activation and then restarts the
owner.

```text
owner generation 1 ── owns ──> owner/child generation 1
        Restart
owner generation 2 ── owns ──> owner/child generation 2
```

## Run

From the repository root:

```sh
go run ./examples/ownership
```

Expected output:

```text
start owner / generation 1
start owner/child / generation 1
close owner/child / generation 1
close owner / generation 1
start owner / generation 2
start owner/child / generation 2
close owner/child / generation 2
close owner / generation 2
```

## How it works

The owner calls `scope.Env().Mount` during `Activate`. That Env assigns the
child an owner-qualified ID and binds it to the current owner generation.

`Instance.Restart` first cleans up the old subtree in child-before-owner order.
The next owner generation runs `Activate` again and declares a new child. Final
`Host.Shutdown` cleans up the second subtree in the same order.

See [main.go](main.go) for the complete program and
[readiness](../readiness/README.md) for a child whose Service availability is
independent from its owner's readiness.
