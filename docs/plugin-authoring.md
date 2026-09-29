# Writing Plugins

English | [中文](plugin-authoring.zh.md)

This guide covers the current Local-first author model. See [Core Concepts](concepts.md)
for the object model and [Lifecycle](lifecycle.md) for cleanup rules.

## 1. Define a shared contract

Providers and consumers depend on a small shared package:

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

Use `StoreKey.Spec()` in `PluginSpec.Requires` and `Provides`. `Provide` submits
the runtime value; `Get` reads the consumer generation's fixed binding.

## 2. Implement Plugin

The plugin struct stores decoded instance configuration and implements stable
`Spec` metadata plus `Activate` for one generation:

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

func (p *Writer) Activate(ctx context.Context, scope *gordis.Scope) error {
    store, err := gordis.Get(scope, StoreKey)
    if err != nil {
        return err
    }
    return store.Put(ctx, p.Path, []byte(p.Text))
}
```

Rules:

- `Spec` ID, Requires, and Provides never depend on configuration.
- `New` returns a fresh non-nil object that implements `Activate`, and only
  sets deterministic defaults.
- `Validate` is repeatable and side-effect free.
- Successful `Activate` means the plugin is usable and has submitted every
  declared Service.
- Long-lived work uses `scope.Context()` through `scope.Go`, not the activation
  context.

During preflight the Host creates a preparation object, checks its `Spec`,
configuration, optional `Validate`, and `Activate` capability, then discards it.
Activation creates a separate runtime object.

## 3. Own resources in Scope

Acquire a resource in `Activate`, register cleanup immediately, and publish it
last:

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

These APIs cover dependencies, work admission, and shutdown cleanup:

- `gordis.Get(scope, key)` is a package-level generic function that obtains this
  generation's fixed dependency during `Activate`; it is not a Scope method.
- `scope.Go(name, fn)` creates and owns a long-running task; a non-cancellation
  error fails the generation. Use `scope.AfterReady(name, fn)` when the task must
  wait until this generation is Ready.
- `scope.Acquire()` leases caller-owned work but does not create a goroutine; the
  caller must `defer release()`. Entrances that only accept calls after Ready use
  `scope.AcquireReady()`.
- `scope.OnStop(name, fn)` closes listeners, routes, subscriptions, and other
  entrances before task cancellation.
- `scope.Defer(name, fn)` releases final resources in reverse order after tasks
  and leases drain.

Do not retain Scope, services, or unmanaged goroutines across generations. If
cleanup registration is rejected, the caller still owns the resource and must
release it immediately.

## 4. Assemble and isolate root instances in the application

The preceding sections are the Plugin author's core path. The application
assembly layer owns type registration, root mounts, and deployment labels. It
registers the fixed Plugin type catalog once, then mounts desired instances:

```go
host, err := gordis.NewHost(storePlugin, new(Writer))
if err != nil {
    return err
}
defer host.Shutdown(context.Background())

tenant := host.Env().Isolate(StoreKey, "tenant-a")
writer, err := tenant.Mount(ctx, gordis.InstanceSpec{
    ID: "writer", Plugin: "writer",
    Config: json.RawMessage(`{"path":"hello","text":"world"}`),
})
if err != nil {
    return err
}
_, err = tenant.Mount(ctx, gordis.InstanceSpec{ID: "store", Plugin: "store"})
if err != nil {
    return err
}
return writer.WaitReady(ctx)
```

The consumer may be mounted first: it stays Pending until a matching provider
appears. `Env.Isolate` applies the same label to that Key's consumer and
provider routes without exposing deployment labels to plugin code.

Use `Instance.Update`, `Restart`, `WaitReady`, and `Unmount` for later control.
Plugins may mount children owned by the current generation through
`Scope.Env()`; stopping or replacing the parent recursively reclaims the old
children. See [Dynamic Management](dynamic.md).

## 5. Opt-in EventBus

Register `&events.Plugin{}` in the Host catalog and mount one
`events.PluginID` instance. Event users declare `events.BusKey.Spec()` in
`Requires` and bind the current generation's Scope:

```go
var Changed = events.NewTopic[string]("example.changed/1")

func (*observerPlugin) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "observer",
        Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
        New:      func() gordis.Plugin { return new(observerPlugin) },
    }
}

func (*observerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
    _, err := events.Bind(scope).On(Changed, observe,
        events.Priority(10), events.Once())
    return err
}
```

Subscriptions belong to the subscriber Scope and are removed during cleanup.
`Publish` uses bounded parallel delivery; `Emit` is stable and sequential;
`Serial`/`Bail` select the first explicitly handled result; `Waterfall`
composes middleware around a `Next` that may be called once and only before the
current middleware returns. A call admitted before return is drained as part of
the dispatch; retaining and calling `Next` afterward returns
`events.ErrNextExpired`. `Env.Isolate` can partition a Topic independently from
Service labels. Use a regular Service when a caller requires a particular
responder.

## 6. Out-of-process plugins

Business plugins use the same `Spec`, `Activate`, `Get`, `Provide`, and Scope
APIs. Assembly uses `processbridge.Adapter`, the child executable uses
`processbridge.Serve`, and explicit `Binding` values define JSON DTO and method
wire behavior.

Register the Adapter with the logical business Plugin ID. Consumers continue to
require the same `Key[T]`; the Adapter publishes a typed proxy during
`Activate`. Only contracts with explicit request/response, cancellation, and
error semantics should cross a process boundary. See
[Process Protocol](process-protocol.md).

Cross-process events likewise leave the business Plugin unchanged: it still
uses `events.Bind(scope)`. Assembly registers matching `BindTopic`/`BindHook`
values, explicit `EventCodec`s, and Publish/Subscribe directions in Adapter and
Serve. Local-only events need no wire. A remote Plugin cannot use
`Scope.Env().Mount` to create a child invisible to the Host.
