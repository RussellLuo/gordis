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

Use `StoreKey.Spec()` in `PluginSpec.Requires` and `Provides`. `scope.Provide`
submits the runtime value; `scope.Get` reads the consumer generation's fixed
binding. `scope.HasProvided(StoreKey.Spec())` reports whether this generation
declared and successfully published that service, not whether another provider
is visible. It returns false for staged values before publication commits.

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
    store, err := scope.Get(StoreKey)
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
- `Config` is one JSON object (an empty value means `{}`); standard struct
  decoding rejects unknown fields.

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
return scope.Provide[Store](StoreKey, store)
```

These APIs cover dependencies, work admission, and shutdown cleanup:

- `scope.Provide(key, value)` stages a declared service for publication after
  `Activate` succeeds. `PluginSpec.Provides` declares the contract;
  `scope.HasProvided(key.Spec())` checks this generation's committed publication.
- `scope.Get(key)` is a generic Scope method that obtains this generation's
  fixed dependency during `Activate`.
- `scope.Lookup(key)` probes an optional service in this generation's
  View without declaring `Requires`, creating a dependency, or acquiring a lease.
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

Do not retain a Scope, its fixed dependencies, or unmanaged goroutines beyond
the owning consumer generation. If cleanup registration is rejected, the caller
still owns the resource and must release it immediately.

### Probe an optional service

Use `Requires + Get` for a required database and `Lookup` for a cache that may
be absent. Inject a query function during `Activate` and call it on each request:

```go
type ValueStore interface {
    Get(context.Context, string) ([]byte, error)
}

var DatabaseKey = gordis.NewKey[ValueStore]("example.database/1")
var CacheKey = gordis.NewKey[ValueStore]("example.cache/1")

type readerPlugin struct {
    database    ValueStore
    lookupCache func() (ValueStore, bool, error)
}

func (*readerPlugin) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "reader",
        Requires: []gordis.ServiceSpec{DatabaseKey.Spec()},
        New:      func() gordis.Plugin { return new(readerPlugin) },
    }
}

func (p *readerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
    database, err := scope.Get(DatabaseKey)
    if err != nil {
        return err
    }
    p.database = database
    p.lookupCache = func() (ValueStore, bool, error) {
        return scope.Lookup(CacheKey)
    }
    return nil
}

func (p *readerPlugin) Get(ctx context.Context, id string) ([]byte, error) {
    cache, found, err := p.lookupCache()
    if err != nil {
        return nil, err
    }
    if found {
        return cache.Get(ctx, id)
    }
    return p.database.Get(ctx, id)
}
```

The query function captures this generation's Scope and performs a fresh Lookup
on each call. Publish the request entrance only after initialization, and accept
requests after Ready. For concurrent entrances, the application must reject new
requests and drain existing ones before reader cleanup; see
[Lifecycle](lifecycle.md) for request leases. This does not extend an optional
cache provider's lifetime. The example falls back only when the cache is absent.
Whether a failed cache call should fall back depends on the business error and
request cancellation.

Run the [optional service example](../examples/optional-service/README.md) to
observe database → cache → database requests in the same reader generation.

`Lookup` returns `(value, found, err)` and only examines the caller's fixed View
label. It never searches another label as a fallback. A missing cache leaves
the reader Ready; cache appearance, removal, or replacement does not restart it.
The Key name and exact Go type must match the provider's contract.

| Result | Meaning |
| --- | --- |
| Zero value, `false`, `nil` | No provider is published and Ready at that label, or it is frozen/stopping |
| Value, `true`, `nil` | The current provider was available at the instant of lookup |
| Zero value, `false`, error | Invalid Key or contract type mismatch; `ErrClosed` for a nil/closed/frozen caller; `ErrStale` for a superseded generation; `ErrLookupUnsupported` for a live detached/process Scope |

The caller must be a current, unfrozen Host generation. `Activate` may query
other Ready providers, but its own staged `Provide` values are not visible
until Ready publication. An undeclared `Get` still returns an error, even if
`Lookup` can find that service.

```text
request obtains cache → provider is unloaded → request calls the old cache
```

New lookups stop returning the unloaded provider. Previously returned objects
remain ordinary Go values: they do not follow replacements or become invalid
automatically. Their methods may succeed or fail because resources were closed
or canceled. The framework neither guarantees those calls nor waits for them
before unloading the provider. Use `Requires + Get` when the provider must
survive through consumer cleanup.

For a background feature that should start and stop with a service, mount a
registered consumer child declaring that service in `Requires`. The child stays
Pending while the service is absent and gets its own lifecycle when it appears.
Its Pending state or failure does not change parent readiness. Ownership does
not imply a readiness dependency. This is a conditional feature; request-time
fallback belongs in `Lookup`.

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

`Lookup` requires a local Host. A Host-backed caller can discover visible
Adapter proxies, but the detached Scope inside the child process returns
`ErrLookupUnsupported`, including for services already accessible through its
fixed `Get` bindings. There is no cross-process dynamic lookup protocol.

Cross-process events likewise leave the business Plugin unchanged: it still
uses `events.Bind(scope)`. Assembly registers matching `BindTopic`/`BindHook`
values, explicit `EventCodec`s, and Publish/Subscribe directions in Adapter and
Serve. Local-only events need no wire. A remote Plugin cannot use
`Scope.Env().Mount` to create a child invisible to the Host.
