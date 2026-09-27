# Typed event publication

This example mounts the optional EventBus and publishes a typed order event to
one subscriber.

## Run

From the repository root:

```sh
go run ./examples/events
```

Expected output:

```text
received order: order-42
```

## How it works

`orderCreatedTopic` is a typed `events.Topic[orderCreated]`. Both application
plugins declare `events.BusKey.Spec()` in `Requires`, so they stay Pending until
the `events.Plugin` instance provides the EventBus.

The observer registers its handler with `events.Bind(scope).On`. The publisher
uses `Scope.AfterReady` because event dispatch is admitted only after its
generation becomes Ready, then calls `Publish` for bounded parallel delivery to
all matching handlers. The main flow waits until the observer receives the
event so the example exits deterministically.

Subscriptions belong to their registering Scope and are removed automatically
when that Scope stops.

See [main.go](main.go) for the complete program and
[Writing Plugins](../../docs/plugin-authoring.md#5-opt-in-eventbus) for Topic,
Hook, ordering, and dispatch semantics.
