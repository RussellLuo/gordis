# Dynamic instance management

This example changes one running Host with `Preview` and `Apply`. A consumer
waits for a missing provider, becomes ready when the provider appears, waits
again when it is removed, and returns with a new generation after `Restore`.

## Run

From the repository root:

```sh
go run ./examples/dynamic
```

Expected output:

```text
consumer: pending (generation 0)
consumer generation 1 reads source generation 1
consumer: ready (generation 1)
consumer: pending (generation 1)
consumer generation 2 reads source generation 2
consumer: ready (generation 2)
```

## How it works

The initial Host is empty because static assembly is strict. A dynamic
`consumer` instance uses `AllowPending`, so it can commit without its required
`Source` service. Adding `source` activates both binding and consumer.

Removing the provider requires `AllowWaitingConsumers`; the consumer returns
to pending instead of being deleted. `Restore` reapplies the removed
description as a new operation, producing new source and consumer generations.

See [main.go](main.go) for the complete program and
[Dynamic Instance Management](../../docs/dynamic.md) for operation semantics.
