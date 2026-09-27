# Lifecycle

English | [中文](lifecycle.zh.md)

Every instance activation creates a new runtime Plugin object, generation, and Scope. The Scope owns the service bindings, tasks, request leases, and cleanup operations for that generation.

See [Core Concepts](concepts.md) for the object model and
[Dynamic Instance Management](dynamic.md) for the public mounting API.

## Startup

```text
pending / stopped
  → generation + 1
  → New → JSON decode → Spec check → Validate
  → create Scope → Plugin.Activate
  → recheck parent and service bindings → publish staged services → ready
```

The Host activates providers before consumers. Before `Activate` returns, a
plugin must complete required handshakes, submit every declared service, and be
ready for use. Long-running work belongs in `Scope.Go`.

Preflight and activation use the same configuration preparation path, but preflight objects are discarded. `New` and `Validate` must not acquire resources; create and register resources only in `Activate`.

On activation failure, the Host preserves the original error and then moves the generation through `stopping → stopped`; staged services never become visible. A child failure cleans up that branch and affected Service consumers, but does not change its owner's readiness. `Instance.WaitReady` reports only the target instance's failure; `Operation.WaitReady` does the same for each explicit operation target.

`Ready` describes only the instance itself. The startup context bounds startup only. Long-running tasks use `scope.Context()`.

## Shutdown

The Host freezes the complete affected closure before invoking user callbacks:

1. Every affected Scope rejects new tasks, leases, and resource registrations.
2. `OnStop` callbacks run in reverse topology to close routes, listeners, subscriptions, and other entrances.
3. The Scope runtime context is canceled.
4. The Host waits for `Scope.Go` tasks and `Acquire` leases to exit.
5. `Defer` callbacks run in reverse order, then services and dependency references are removed.

This cleans up consumers before providers and children before parents. Use `OnStop` only to close entrances; place database connections, processes, and other final resources in `Defer`.

Request entrances should acquire a lease:

```go
release, err := scope.Acquire()
if err != nil {
    return err // the instance is stopping
}
defer release()
```

A raw service reference without a lease is not protected from concurrent shutdown. Cleanup callbacks should use dependencies captured during startup instead of calling `Get` again.

## Timeouts and residuals

An Unmount or Shutdown context bounds only the caller's wait; it does not cancel background cleanup. While a task, request, or callback remains active, Gordis retains the resource, parent generation, and upstream services. The framework cannot forcibly terminate a Go goroutine.

If a cleanup callback returns an error or panics, remaining callbacks still run. `cleanupComplete=true` only means every cleanup step returned; it does not prove that a resource reporting an error was released. An instance with residuals blocks a new generation at the same position to avoid overwriting resources that may still be alive.

Callers observing the same cleanup operation do not run callbacks twice. A managed task or cleanup callback must not synchronously wait for its own Unmount or Shutdown, which would create a self-wait.

## Runtime failure

When a managed task returns a non-cancellation error, or a Plugin/lifecycle callback panics, the Host:

1. Immediately freezes the failing instance, its ownership descendants, and the transitive closure of service consumers.
2. Cancels startup still in progress.
3. Cleans up affected instances through the serialized lifecycle executor.

The source follows the normal cleanup `Phase` while independently retaining `Failure`, `FailurePhase`, and the final `Outcome=failed`. A dependent instance without its own error returns to `pending` after cleanup, with a `StopReason` pointing to the source, and activates with a new generation when the dependency recovers. Unrelated instances keep running. A cleanup-only error leaves `Failure` empty and produces `Outcome=incomplete`, with details in `CleanupError` and `Residuals`.

Goroutines not started through `Scope.Go` are outside Gordis failure capture and cleanup.

## Management concurrency and diagnostics

Lifecycle operations are serialized. Conflicting operations return `ErrBusy`; Shutdown waits for an accepted operation before draining the Host. User callbacks run outside the Host state lock and may read Snapshot, but they must not reenter and wait for a new lifecycle operation.

Important diagnostic fields:

| Field | Meaning |
| --- | --- |
| `Phase` | Generation lifecycle position and whether this instance is ready |
| `Generation` / `ParentGeneration` | Current run and owning parent run |
| `Bindings` / `ProvidedLabels` | Fixed input providers and provided service labels |
| `BlockedBy` / `BlockedLabels` | Missing dependencies or resources still in use |
| `Tasks` / `Leases` | Work and admitted requests still active |
| `Failure` / `FailurePhase` / `Outcome` | Original runtime failure, its stage, and the final generation result |
| `CleanupPhase` / `CleanupComplete` / `CleanupError` / `Residuals` | Cleanup progress, errors, and failed items |
| `StopReason` | Propagated owner, dependency, or explicit-operation stop cause |

Dynamic changes use the same freeze, drain, and residual rules; see [Dynamic Instance Management](dynamic.md). `processbridge` reuses the same Scope lifecycle and coordinates remote cleanup through `gordis.process/1`.
