# Dynamic instance management

English | [中文](README.zh.md)

This example changes one running Host through a `ChangeSet`. It removes the
provider of a ready consumer, observes the consumer return to `pending`, and
uses `Operation.Restore` to bring both back with new generations.

## Run

From the repository root:

```sh
go run ./examples/dynamic
```

Expected output:

```text
consumer generation 1 reads source generation 1
consumer pending: source removed
consumer generation 2 reads source generation 2
```

## How it works

`NewHost` registers a fixed plugin type catalog. The root Env mounts `source`
and `consumer` and waits until both are ready, establishing the running state
that the control-plane change will modify.

Removing the provider uses `ChangeSet.Unmount` followed by `ChangeSet.Apply`;
the surviving consumer returns to pending instead of being deleted. The
returned durable `Operation` is first awaited for convergence, then `Restore`
reapplies the removed description as a new operation, producing new source and
consumer generations. Consumer activation prints each ready generation; after
the removal operation completes, the main flow reports the intentional
`pending` transition without polling diagnostics.

See [main.go](main.go) for the complete program and
[Dynamic Instance Management](../../docs/dynamic.md) for operation semantics.
