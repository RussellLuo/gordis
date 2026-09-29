# Core Concepts

English | [中文](concepts.zh.md)

Gordis models an application as a graph of plugin instances. Plugins collaborate through typed services, the Host validates the graph and coordinates lifecycle operations, and each Scope owns the tasks, requests, and resources of one run.

## Concept map

```text
Contract definition
Plugin prototype ──Spec()──> PluginSpec
Key[T] ──Spec()──> ServiceSpec ──declared in──> PluginSpec.Requires / Provides

Application assembly
NewHost(Plugin types) ──> Host ──> synthetic root Env
Env View + InstanceSpec(ID / Plugin / Config) ──Mount──> desired instance graph
Host ──validates──> Requires / Provides + View labels: service dependency edges
Host ──activates──> instance generation
                    ├─ runtime Plugin (created by PluginSpec.New())
                    ├─ Scope ──owns──> tasks / leases / cleanup
                    └─ fixed binding: consumer Key + label → provider instance + generation → service value
```

## From a plugin type to one run

| Concept | Meaning |
| --- | --- |
| Plugin prototype | Passed to `NewHost`; used only to read stable metadata from `Spec()` and never started |
| `PluginSpec` | Plugin type ID, `New`, `Requires`, and `Provides` |
| `Env` | Immutable owner and visibility View used to mount instances |
| `InstanceSpec` | Owner-local ID, selected plugin type, and JSON configuration |
| `Instance` | Stable logical handle across Update/Restart generations until Unmount |
| Runtime Plugin object | A fresh object created by `PluginSpec.New()` for one preparation or activation |
| generation | The activation attempt of one instance; incremented for every real activation |
| `Scope` | Identity, fixed dependencies, tasks, leases, and cleanup for one generation |
| `Host` | Validates the graph, assigns generations, publishes services, coordinates lifecycle operations, and reports diagnostics |

One plugin type can create multiple independently configured instances. Restarting an instance keeps its ID but creates a new runtime object, Scope, and generation. Results from an old generation cannot mutate a newer one.

The Host uses the same preparation path for preflight and activation:

```text
New → JSON decode → Spec contract check → optional Validate → Activate capability check
```

Preflight objects are discarded, so `New` and `Validate` must be free of side effects. Resources may only be created in `Activate`.

## Services, keys, and labels

A service is a Go value shared between plugins, usually through an interface. Define its `Key[T]` in a small shared contract package:

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`Key[T].Spec()` exposes the `ServiceSpec` metadata stored in
`PluginSpec.Requires` and `PluginSpec.Provides`.

A provider declares `StoreKey.Spec()` in `PluginSpec.Provides` and calls `Provide` in `Activate`. A consumer declares the same contract in `Requires` and obtains its fixed binding with `Get`.

A Plugin is not a Service, and `Provides` does not publish the Plugin automatically. A plugin may publish itself, another object, or several service values.

By default, the service name is also its visibility label. Applications derive
an isolated Env when several providers implement the same contract:

```text
tenantA := host.Env().Isolate(StoreKey, "tenant-a")
tenantB := host.Env().Isolate(StoreKey, "tenant-b")
```

Plugin code still uses only `StoreKey`; it does not know visibility labels. The
Host requires one provider per `(Key, label)` and an exact contract-name and Go
type match at both ends.

## Ownership, service dependencies, and visibility

Gordis has three independent relationships:

| Relationship | Expressed by | What it controls |
| --- | --- | --- |
| Lifecycle ownership | The `Env` used to `Mount` | Generation affiliation, recursive stop, and child-first cleanup |
| Service dependency | `PluginSpec.Requires` / `Provides` | Pending, activation, fixed binding, and consumer-first cleanup |
| Service visibility | `Env.Isolate(Key, label)` | Which `(Key, label)` providers are eligible below that Env |

Instances mounted from `host.Env()` belong to the synthetic root; children
mounted from `scope.Env()` belong to the current parent generation. Ownership
does not imply readiness: a pending or failed child does not change its owner's
phase or `WaitReady` result unless the owner explicitly requires a Service from
it. Isolate derives an immutable visibility View; it does not create a new
lifecycle owner.

## State, readiness, and waiting

The primary generation lifecycle uses these `Phase` values:

```text
pending ──dependency appears──> activating ──> ready ──> stopping ──> stopped
                                      │                         ▲
                                      └──────── failure ────────┘
```

Failure is not a `Phase`; it is recorded independently from final `Outcome` and cleanup diagnostics.

- `ready` means the instance itself has activated and published its services.
- A missing required binding automatically leaves the desired Instance in `pending`; no instance-level opt-in exists.
- `Snapshot` reports Phase, Failure/Outcome, bindings, generation, blocked labels, tasks, leases, stop reasons, and cleanup results.

Commit and readiness are different. `Env.Mount` can commit successfully while
an instance remains pending; use `Instance.WaitReady` only when the next step
requires that exact desired revision. `Operation.WaitReady` checks every exact
target pinned by the operation, but does not recursively add their children.

## Application readiness gates

When an application needs a minimum set of capabilities, mount an ordinary
plugin that requires those Services, then call `MountReady` for that gate. The
gate remains pending while any required Service is unavailable. Startup tooling
can inspect provider failures and `Snapshot.BlockedBy`/`BlockedLabels` for
diagnostics; ownership-subtree health aggregation is not a core state.

## Scope and resource ownership

A Scope belongs to exactly one generation. Its APIs form one path from dependency
access and work admission to shutdown cleanup:

| Role | API | Semantics |
| --- | --- | --- |
| Read a dependency | `gordis.Get(scope, key)` | Read the Service binding fixed when this generation started |
| Manage a long-running task | `scope.Go` / `scope.AfterReady` | Start a Scope-supervised task; the latter waits for this generation to become Ready |
| Protect admitted work | `scope.Acquire` / `scope.AcquireReady` | Hold a manually released request lease; the latter also rejects calls before Ready |
| Withdraw entrances | `scope.OnStop` | Close listeners, routes, subscriptions, and other entrances before task cancellation |
| Release final resources | `scope.Defer` | Release resources in reverse order after tasks and leases exit |

`Get` is a package-level generic function that receives a Scope, not a Scope
method. The Scope fixes its binding and lifetime. `Go` creates and supervises a
goroutine; `Acquire` does not create one, but admits caller-owned work into the
same shutdown drain barrier. Long-running tasks use the Scope runtime context,
not the startup context. Service references, Scopes, and background goroutines
must not escape into cross-generation global state. See
[Lifecycle](lifecycle.md) for the exact shutdown sequence.

## Out-of-process execution

Crossing a process boundary does not change the business contract. A native provider publishes a real Go object; `processbridge.Adapter` publishes a typed proxy under the same `Key[T]`:

```text
consumer ── Get(GreeterKey) ──> local implementation
                            └─> typed proxy ── RPC ──> remote implementation
```

The consumer does not know which process owns the implementation. Process assembly still needs an explicit wire binding because a service name alone cannot define parameter encoding, error mapping, or cancellation semantics. See [Process Protocol](process-protocol.md).

## Boundaries

Gordis core owns the instance graph, service bindings, generations, and lifecycle. It does not implement HTTP, UI, agents, package downloads, persistence, or a security sandbox. Applications can build package registries, management APIs, and frontend extension systems on top.
