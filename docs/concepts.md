# Core Concepts

English | [中文](concepts.zh.md)

Gordis models an application as a graph of plugin instances. Plugins collaborate through typed services, the Host validates the graph and coordinates lifecycle operations, and each Scope owns the tasks, requests, and resources of one run.

## Concept map

```text
Contract definition
Plugin prototype ──Spec()──> PluginSpec
Key[T] ──Spec()──> ServiceSpec ──declared in──> PluginSpec.Requires / Provides

Application assembly
InstanceSpec
├─ Plugin field ──selects──> PluginSpec.ID
├─ ID / Config / Parent / Optional
└─ Inputs / Outputs ──map service contracts to slots

PluginSpec + InstanceSpec ──> Host
Host ──validates──> instance graph
                    ├─ Parent: ownership edges
                    └─ Requires / Provides + slots: service dependency edges
Host ──activates──> instance generation
                    ├─ runtime Plugin (created by PluginSpec.New())
                    ├─ Scope ──owns──> tasks / leases / cleanup
                    └─ fixed binding: consumer Key → slot → provider instance + generation → service value
```

## From a plugin type to one run

| Concept | Meaning |
| --- | --- |
| Plugin prototype | Passed to `NewHost`; used only to read stable metadata from `Spec()` and never started |
| `PluginSpec` | Plugin type ID, `New`, `Requires`, and `Provides` |
| `InstanceSpec` | Instance ID, selected plugin type, JSON configuration, and assembly relationships |
| Runtime Plugin object | A fresh object created by `PluginSpec.New()` for one preparation or activation |
| generation | The activation attempt of one instance; incremented for every real activation |
| `Scope` | Identity, fixed dependencies, tasks, leases, and cleanup for one generation |
| `Host` | Validates the graph, assigns generations, publishes services, coordinates lifecycle operations, and reports diagnostics |

One plugin type can create multiple independently configured instances. Restarting an instance keeps its ID but creates a new runtime object, Scope, and generation. Results from an old generation cannot mutate a newer one.

The Host uses the same preparation path for preflight and activation:

```text
New → JSON decode → Spec contract check → optional Validate
```

Preflight objects are discarded, so `New` and `Validate` must be free of side effects. Resources may only be created in `Start`.

## Services, keys, and slots

A service is a Go value shared between plugins, usually through an interface. Define its `Key[T]` in a small shared contract package:

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`Key[T].Spec()` exposes the `ServiceSpec` metadata stored in
`PluginSpec.Requires` and `PluginSpec.Provides`.

A provider declares `StoreKey.Spec()` in `PluginSpec.Provides` and calls `Provide` in `Start`. A consumer declares the same contract in `Requires` and obtains its fixed binding with `Get`.

A Plugin is not a Service, and `Provides` does not publish the Plugin automatically. A plugin may publish itself, another object, or several service values.

By default, the service name is also its slot name. Applications use `Outputs` and `Inputs` when several providers implement the same contract:

```text
store-main  ── example.store/1 → store.primary ──> collector-main
store-audit ── example.store/1 → store.audit   ──> collector-audit
```

Plugin code still uses only `StoreKey`; it does not know deployment slot names. The Host requires one provider per slot and an exact contract-name and Go-type match at both ends.

## Ownership and service dependencies

Gordis tracks two independent relationships:

- `InstanceSpec.Parent` expresses lifecycle ownership: which instance should stop with which parent.
- `PluginSpec.Requires` expresses service dependency: which provider must start first and clean up last.

Neither relationship replaces the other. A child does not gain access to its parent's services automatically, and unrelated instances may still collaborate through services. The Host combines both relationships into one acyclic graph, starts parents and providers first, and cleans up children and consumers first.

`Optional: true` applies only to children and excludes that subtree from the parent's `GroupReady` result. It does not relax service validation or protect other consumers that depend on the optional plugin.

## State, readiness, and waiting

Common instance states are:

```text
registered → starting → ready → stopping → stopped
               │                    │
               └──── failed ────────┘
pending ─────────dependency appears─> starting
```

- `Ready` means the instance itself has started and published its services.
- `GroupReady` also requires every non-optional child to be ready.
- `AllowPending` lets a dynamically submitted consumer wait for a missing service; static `NewHost` assembly still requires a complete graph.
- `Snapshot` reports bindings, generation, blocked slots, tasks, leases, stop reasons, and cleanup results.

Operation completion and instance readiness are different. A dynamic change can commit successfully while an instance remains pending; use `WaitOperation` and `WaitReady` for the two conditions.

## Scope and resource ownership

A Scope belongs to exactly one generation:

- `Get` reads the generation's fixed service binding.
- `Go` starts a managed task.
- `Acquire` holds a lease for an admitted request.
- `OnStop` closes public entrances first.
- `Defer` releases final resources in reverse order after tasks and leases exit.

Long-running tasks use the Scope runtime context, not the startup context. Service references, Scopes, and background goroutines must not escape into cross-generation global state. See [Lifecycle](lifecycle.md) for the exact sequence.

## Out-of-process execution

Crossing a process boundary does not change the business contract. A native provider publishes a real Go object; `processbridge.Adapter` publishes a typed proxy under the same `Key[T]`:

```text
consumer ── Get(GreeterKey) ──> local implementation
                            └─> typed proxy ── RPC ──> remote implementation
```

The consumer does not know which process owns the implementation. Process assembly still needs an explicit wire binding because a service name alone cannot define parameter encoding, error mapping, or cancellation semantics. See [Process Protocol](process-protocol.md).

## Boundaries

Gordis core owns the instance graph, service bindings, generations, and lifecycle. It does not implement HTTP, UI, agents, package downloads, persistence, or a security sandbox. Applications can build package registries, management APIs, and frontend extension systems on top.
