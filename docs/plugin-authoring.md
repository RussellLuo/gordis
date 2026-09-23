# Writing Plugins

English | [中文](plugin-authoring.zh.md)

This guide follows the shortest path for plugin authors. See [Core Concepts](concepts.md) for the object model and [Lifecycle](lifecycle.md) for exact startup and cleanup rules.

## 1. Define a shared contract

Providers and consumers should depend on a small shared contract package:

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

Use `StoreKey.Spec()` in `Requires` and `Provides`, and `StoreKey.Name()` in `Inputs` and `Outputs` slot mappings. The actual service value is still submitted with `Provide`.

## 2. Implement Plugin

The plugin struct holds instance configuration and implements a stable `Spec()` plus `Start()` for one generation:

```go
type Writer struct {
    Path string `json:"path"`
    Text string `json:"text"`
}

func (*Writer) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "writer",
        Requires: []gordis.ServiceSpec{StoreKey.Spec()},
        New:      func() gordis.Plugin { return new(Writer) },
    }
}

func (p *Writer) Validate() error {
    if p.Path == "" {
        return errors.New("path is required")
    }
    return nil
}

func (p *Writer) Start(ctx context.Context, scope *gordis.Scope) error {
    store, err := gordis.Get(scope, StoreKey)
    if err != nil {
        return err
    }
    return store.Put(ctx, p.Path, []byte(p.Text))
}
```

Register one prototype and create instances with `InstanceSpec`:

```go
host, err := gordis.NewHost(
    []gordis.Plugin{new(Writer), storePlugin},
    []gordis.InstanceSpec{{
        ID: "writer-main", Plugin: "writer",
        Config: json.RawMessage(`{"path":"hello","text":"world"}`),
    }},
)
```

Follow these rules:

- The ID, Requires, and Provides returned by `Spec()` must not depend on configuration.
- `New()` returns a fresh non-nil object every time and may only set deterministic defaults.
- `Validate()` is repeatable and side-effect free; it must not open resources or start work.
- A successful `Start()` means the plugin is ready and has submitted every declared service.
- Plugin and Service are separate concepts; a plugin may publish itself or another object.

The Host creates, decodes, and validates separate objects for preflight and activation. Every preflight object is discarded.

## 3. Provide services and own resources

Create a resource in `Start`, register cleanup immediately, and publish the service last:

```go
store, err := openStore()
if err != nil {
    return err
}
if err := scope.Defer("store", func(context.Context) error {
    return store.Close()
}); err != nil {
    _ = store.Close()
    return err
}
return gordis.Provide[Store](scope, StoreKey, store)
```

Common Scope APIs:

- `scope.Go(name, fn)` starts a managed task; a non-cancellation error fails the instance.
- `scope.OnStop(name, fn)` closes listeners, routes, subscriptions, or other entrances.
- `scope.Acquire()` protects an admitted request; shutdown waits for its lease to release.
- `scope.Defer(name, fn)` releases final resources in reverse order.
- `scope.Context()` is the runtime context for long-lived work.

Do not use the startup context for long-lived tasks, and do not store services, Scopes, or unmanaged goroutines in cross-generation global state. If cleanup registration fails, the caller still owns the resource and must release it immediately.

## 4. Assemble multiple instances

Applications map slots when several instances provide the same contract:

```go
specs := []gordis.InstanceSpec{
    {ID: "store-main", Plugin: "store", Outputs: map[string]string{StoreKey.Name(): "store.primary"}},
    {ID: "store-audit", Plugin: "store", Outputs: map[string]string{StoreKey.Name(): "store.audit"}},
    {ID: "writer-main", Plugin: "writer", Inputs: map[string]string{StoreKey.Name(): "store.primary"}},
    {ID: "writer-audit", Plugin: "writer", Inputs: map[string]string{StoreKey.Name(): "store.audit"}},
}
```

Plugin code knows only the shared Key, not deployment slot names. `Parent` expresses lifecycle ownership and does not replace Requires. An auxiliary child may set both `Parent` and `Optional: true`; its failure does not make the parent group's `GroupReady` false, but `Start` still reports the error and service dependencies remain required.

## 5. Dynamic instances

A running Host uses `Preview` and `Apply` to submit complete `InstanceSpec` values. A consumer that may wait for a missing service must set `AllowPending`; removing a provider while keeping consumers also requires `AllowWaitingConsumers` on the `Change`.

A change may commit while an instance is still waiting, so use `WaitOperation` and `WaitReady` separately. See [Dynamic Instance Management](dynamic.md).

## 6. Out-of-process plugins

The business Plugin still uses only the root package's `Spec`, `Start`, `Get`, `Provide`, and Scope APIs. Assembly uses `processbridge.Adapter`, the child executable uses `processbridge.Serve`, and both sides describe cross-process methods and JSON DTOs with explicit `Binding` values.

Register the Adapter with the logical business plugin ID, not an execution-specific ID such as `greeter-process`. Consumers continue to require the same `Key[T]`; the Adapter publishes the proxy during `Start`.

Only contracts with clear request/response, cancellation, and error semantics should cross the process boundary. Process exit does not prove successful business cleanup; see [Process Protocol](process-protocol.md).

Applications that deliver their own backend and UI after Host startup can implement one stable application contract. The remote Plugin starts its HTTP, gRPC, or other data plane inside its Scope and publishes private listeners through an `Endpoint` service. The Host supplies only a generic process Adapter, protocol Gateway, and UI Loader; it does not know application routes. Data-plane requests must still hold remote Scope leases so `OnStop` closes admission before the server drains and disposes. See [application-plugin](../examples/application-plugin/README.md).

## Examples

| Example | Focus |
| --- | --- |
| [basic](../examples/basic/README.md) | Shared services and instance configuration |
| [composition](../examples/composition/README.md) | Slots, Parent, and multiple instances |
| [optional](../examples/optional/README.md) | Optional subtrees and group readiness |
| [lifecycle](../examples/lifecycle/README.md) | Tasks, leases, and cleanup order |
| [dynamic](../examples/dynamic/README.md) | Runtime assembly and restore |
| [process](../examples/process/README.md) | Typed process service |
| [duplex](../examples/duplex/README.md) | Bidirectional Host/plugin calls |
| [application-plugin](../examples/application-plugin/README.md) | Application-owned data plane and Host UI |
