# Lifecycle

English | [中文](lifecycle.zh.md)

Every instance activation creates a new runtime Plugin object, generation, and Scope. The Scope owns the service bindings, tasks, request leases, and cleanup operations for that generation.

Run `go run ./examples/lifecycle` to observe entrance closure, lease draining, and resource release. See [Core Concepts](concepts.md) for the object model.

## Startup

```text
registered / pending / stopped
  → generation + 1
  → New → JSON decode → Spec check → Validate
  → create Scope → Plugin.Start
  → recheck parent and service bindings → publish staged services → ready
```

The Host starts parents and providers first. Before `Start` returns, a plugin must complete required handshakes, submit every declared service, and be ready for use. Long-running work belongs in `Scope.Go`.

Preflight and activation use the same configuration preparation path, but preflight objects are discarded. `New` and `Validate` must not acquire resources; create and register resources only in `Start`.

On startup failure, the Host cleans up instances started by that operation. The failed instance retains `failed` and its original error, and staged services never become visible. Failure in an optional subtree cleans up that branch and affected consumers while allowing a healthy parent group to remain available, but the startup call still returns the error.

`Ready` describes the instance itself; `GroupReady` also requires every non-optional child to be ready. The startup context bounds startup only. Long-running tasks use `scope.Context()`.

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

The Stop context bounds only the caller's wait; it does not cancel background cleanup. While a task, request, or callback remains active, Gordis retains the resource, parent generation, and upstream services. The framework cannot forcibly terminate a Go goroutine.

If a cleanup callback returns an error or panics, remaining callbacks still run. `cleanupComplete=true` only means every cleanup step returned; it does not prove that a resource reporting an error was released. An instance with residuals blocks a new generation at the same position to avoid overwriting resources that may still be alive.

Repeated Stop calls wait for the same cleanup operation and do not run callbacks twice. A managed task or cleanup callback must not synchronously wait for its own Stop, which would create a self-wait.

## Runtime failure

When a managed task returns a non-cancellation error, or a Plugin/lifecycle callback panics, the Host:

1. Immediately freezes the failing instance, its ownership descendants, and the transitive closure of service consumers.
2. Cancels startup still in progress.
3. Cleans up affected instances through the serialized lifecycle executor.

The source retains `failed`, `LastError`, and `FailurePhase`. A dependent instance without its own error becomes `stopped` after cleanup, with a `StopReason` pointing to the source. Healthy consumers with `AllowPending` may return to `pending` and activate with a new generation when the dependency recovers. Unrelated instances keep running.

Goroutines not started through `Scope.Go` are outside Gordis failure capture and cleanup.

## Management concurrency and diagnostics

Lifecycle operations are serialized. Conflicting operations return `ErrBusy`; matching Stop requests join the existing cleanup. User callbacks run outside the Host state lock and may read Snapshot, but they must not reenter and wait for a new lifecycle operation.

Important diagnostic fields:

| Field | Meaning |
| --- | --- |
| `State` / `GroupReady` | Availability of the instance and its required subtree |
| `Generation` / `ParentGeneration` | Current run and owning parent run |
| `Bindings` / `ProvidedSlots` | Fixed input providers and output slots |
| `BlockedBy` / `BlockedSlots` | Missing dependencies or resources still in use |
| `Tasks` / `Leases` | Work and admitted requests still active |
| `CleanupPhase` / `CleanupComplete` / `Residuals` | Cleanup progress and failures |
| `LastError` / `FailurePhase` / `StopReason` | Failure source and propagated stop reason |

Dynamic changes use the same freeze, drain, and residual rules; see [Dynamic Instance Management](dynamic.md). `processbridge` reuses the same Scope lifecycle and coordinates remote cleanup through `gordis.process/1`.
