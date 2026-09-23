# Dynamic Instance Management

English | [中文](dynamic.zh.md)

`Preview` and `Apply` can add, update, disable, or remove instances in one running Host. The plugin type catalog remains fixed when `NewHost` is created; dynamic operations change instance descriptions and configuration.

Run `go run ./examples/dynamic` to observe a consumer moving through `pending → ready → pending → ready` and receiving a new generation after restore.

## Submit a change

```go
plan, err := host.Preview(gordis.Change{
    Upsert: []gordis.InstanceSpec{{
        ID: "collector-alpha", Plugin: "collector",
        AllowPending: true,
        Inputs: map[string]string{SamplerKey.Name(): "sampler.alpha"},
    }},
})
if err != nil {
    return err
}
operationID, err := host.Apply(ctx, plan)
```

`Upsert` contains complete instance descriptions; fields are not merged. Submitting the same description again is an explicit rebuild or retry. `Disabled` records desired state, and recovering a dependency does not enable an instance that the application disabled.

| API | Purpose |
| --- | --- |
| `Preview(Change)` | Copy configuration, validate the candidate graph, and compute impact without changing runtime state |
| `Apply(ctx, plan)` | Reject a stale plan, drain old generations, and commit the candidate graph |
| `Operation(id)` | Inspect phase, affected instances, target versions, errors, and restore linkage |
| `WaitOperation(ctx, id)` | Wait for the operation; caller timeout does not cancel background cleanup |
| `WaitReady(ctx, id)` | Wait for one instance to become ready or fail itself |
| `Restore(ctx, id)` | Submit the complete old description as a new restore operation |

A plan is bound to one Host and graph revision. Another management change makes it stale and `Apply` returns `ErrStale`. Management operations are serialized; overlap returns `ErrBusy`.

## Pending consumers and provider removal

Static `NewHost` assembly requires every dependency. In a dynamic graph, only a consumer with `AllowPending` may wait for a missing service.

Removing a provider while keeping consumers also requires explicit permission on the change:

```go
plan, err := host.Preview(gordis.Change{
    Remove: []string{"sampler-alpha"},
    AllowWaitingConsumers: true,
})
```

The Host freezes and drains consumers before withdrawing the provider. Healthy consumers enter `pending`, and `BlockedSlots` identifies missing slots. When a replacement appears, the Host validates the complete graph again and activates consumers with new generations.

`Optional` affects parent group readiness; it does not allow missing dependencies. Preview rejects a missing parent, contract conflict, duplicate slot provider, or a cycle in the combined ownership and dependency graph.

## Commit, readiness, and restore

An operation moves through:

```text
draining → starting → completed / failed
```

`Committed` reports whether the candidate description became the current graph. `completed` may include intentional pending instances and does not mean that every instance is ready. Management code should wait for the operation first, then wait for specific instances when required.

The Host computes the affected closure from both old and new graphs. It freezes the entire closure before the first stop callback. Unaffected instances preserve their Scope and generation. A service slot or new generation cannot replace old tasks, leases, or residuals that have not been cleaned up.

`Restore` is not a transactional rollback. It submits the old complete description as a new change, can fail again, and uses a new generation on success. A later management change makes an old restore record stale.

## Boundaries

The current instance graph, operation records, and generation history are in memory only. Gordis does not provide persistence, unlimited retry, in-place hot configuration, parallel old/new generations, business-side-effect rollback, or forced Go goroutine termination.

See [dynamic.go](../dynamic.go) and [dynamic_test.go](../dynamic_test.go) for implementation and behavior tests. Resource cleanup follows [Lifecycle](lifecycle.md).
