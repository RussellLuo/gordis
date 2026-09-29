# Dynamic Instance Management

English | [中文](dynamic.zh.md)

`NewHost` fixes the statically linked Plugin type catalog and creates an empty
synthetic root. `NewHost` accepts only the type catalog; applications mount all
instances through `host.Env()`.

## Mount and wait

```go
host, err := gordis.NewHost(plugins...)
if err != nil {
    return err
}
defer host.Shutdown(context.Background())

collector, err := host.Env().Mount(ctx, gordis.InstanceSpec{
    ID: "collector", Plugin: "collector", Config: collectorConfig,
})
if err != nil {
    return err // validation failed; nothing was committed
}
if err := collector.WaitReady(ctx); err != nil {
    return err
}
```

`Mount` returns after the desired instance is committed, not after activation.
`MountReady` is the `Mount` plus `WaitReady` convenience form. If readiness
waits or activation fails, `MountReady` returns the non-nil committed Instance
with the error; it never silently unmounts it.

| API | Meaning |
| --- | --- |
| `Env.Mount` | Validate and commit a root instance, then converge asynchronously |
| `Env.MountReady` | Mount and wait for that exact desired revision itself |
| `Instance.Update` | Commit new JSON configuration and replace the generation |
| `Instance.Restart` | Replace the generation with unchanged configuration |
| `Instance.WaitReady` | Wait for the revision current when the call begins |
| `Instance.Unmount` | Remove desired state and wait for cleanup within the caller context |
| `Host.Shutdown` | Permanently close the root Env and drain all instances |

## Pending and dependency recovery

A missing required Service is normal dynamic state. The consumer is committed
as `pending` without an instance-level opt-in. Mounting a matching provider
activates it automatically. If a provider fails at runtime, healthy consumers
are frozen and drained, return to `pending`, and receive new generations after
the provider is explicitly updated or restarted.

Plugin activation failure is different: Gordis records the original failure
and cleans the generation, but does not retry that same logical instance until
the application calls `Update` or `Restart`.

## Isolated service views

The zero View routes a Service Key to its own name. `Env.Isolate` immutably
replaces the label for one target:

```go
tenantA := host.Env().Isolate(StoreKey, "tenant-a")

reader, err := tenantA.Mount(ctx, gordis.InstanceSpec{
    ID: "reader", Plugin: "reader",
})
_, err = tenantA.Mount(ctx, gordis.InstanceSpec{
    ID: "store", Plugin: "store",
})
```

Both instances resolve `StoreKey` at `(StoreKey.Name(), "tenant-a")`. A default
provider does not satisfy the isolated reader, and providers under different
labels do not conflict. The chosen binding and provider generation remain fixed
for one consumer generation.

## Commit and handle rules

Configuration is decoded and validated before an operation is accepted. A
canceled context before acceptance commits nothing. After acceptance, root
operations wait until the commit result is known rather than returning an
ambiguous timeout.

`WaitReady` pins the logical identity and exact desired revision, and observes
only that instance rather than its ownership descendants. A later
Update or Restart returns `ErrStale` to the old wait; Unmount returns
`ErrClosed`. An unmounted handle never starts referring to a later instance
that reuses the same local ID.

An `Instance.Unmount` context bounds only the caller's wait. Once the operation
is accepted, cleanup continues in the background. Callers that need durable
observation after a timeout should use a `ChangeSet` and retain its `Operation`.

Unmounting a provider does not delete otherwise surviving consumers. If a
required binding becomes unresolved, those consumers are drained and enter
`pending`, just as they do after a provider runtime failure. Mounting a matching
provider lets the Host converge them again.

## Bulk changes and recovery (advanced)

Ordinary applications should prefer the `Env` and `Instance` operations above.
Loaders and other control planes that must atomically change multiple instances
use `ChangeSet.Apply → Operation`:

```go
changes := host.Changes()
changes.Mount(tenantEnv, gordis.InstanceSpec{
    ID: "store", Plugin: "store", Config: storeConfig,
})
changes.Update(reader, readerConfig)

operation, err := changes.Apply(ctx)
if err != nil {
    return err
}
mounted := operation.Mounted() // ChangeSet.Mount registration order
if err := operation.WaitReady(ctx); err != nil {
    return err
}
```

`ChangeSet.Apply` validates the complete candidate before freezing any existing
scope and guards the commit with the current Host revision, Instance logical
identities, and owner generations. These checks are framework invariants, not a
manual authorization step. A nil error means the candidate graph committed;
activation may still be running or normally Pending on a missing required
binding. `Operation.Wait` observes convergence completion, while
`Operation.WaitReady` waits for all exact revisions explicitly pinned by that
operation to become Ready; it does not recursively wait for their children.

If the caller context expires after acceptance, `Apply` returns both a non-nil
`Operation` and the context error. A control plane that needs observation after
that timeout retains the Operation and calls `Wait` with a fresh context;
ordinary request paths can return the error directly.

The affected closure remains available from `Operation.Snapshot` for
diagnostics and application-level audit. An application that needs approval or
dry-run policy can build that workflow above Gordis without making every local
change pay for it.

Unmounting an old Instance and mounting the same local ID from the same owner
Env in one ChangeSet is an atomic replacement. The new Instance has a new
logical identity and the old handle remains permanently closed.
`Operation.Restore` is a new explicit operation: it restores only descriptions
directly changed by that operation, and only while the Host revision and owner
generation still match, so it cannot overwrite newer Loader or user intent.
Descendants removed recursively when their owner stopped belong to the old
generation and are not directly revived. A Plugin redeclares its children from
the new `Scope.Env`; an external Loader reconciles them from the new
`parent.Env()` after the restored parent is Ready.

A Loader retains its entries, owner Envs, and Instances. Group, include,
overlay, package, and disabled semantics are application configuration policy:
a disabled entry becomes an Unmount and re-enabling it performs a fresh Mount.
The core neither reads configuration files nor maintains a second Loader
runtime graph.

Snapshots expose local/qualified identity, phase, generation, fixed bindings,
blocked labels, failure/outcome, and cleanup state. See [Lifecycle](lifecycle.md)
for resource ordering and residual rules.
