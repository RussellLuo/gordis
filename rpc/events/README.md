# RPC events

[English](README.md) | [中文](README.zh.md)

`github.com/RussellLuo/gordis/rpc/events` runs typed Gordis notifications and Hooks through one Host Bus. Business plugins use ordinary `events.Bind(scope).On`, `Publish`/`Parallel`, and `Emit`. Each Child has its own immutable registry and Session-owned peer. Local→Local, Host→Child, Child→Host, Child→Child and Child self-delivery all use Host routing.

## Assembly

Define Topics in an ordinary contract package. Keep codecs and process selection in assembly code:

```go
import (
    core "github.com/RussellLuo/gordis/events"
    "github.com/RussellLuo/gordis/rpc"
    rpcevents "github.com/RussellLuo/gordis/rpc/events"
)

var Notice = core.NewTopic[string]("example/notice")

bridge, err := rpcevents.NewBridge(
    rpcevents.BindTopic(Notice, rpcevents.JSONEventCodec[string]("example/notice-json/1"),
        rpcevents.Publish|rpcevents.Subscribe),
)
// Check err before using bridge.

// Host: ordinary business imports/exports only. The bridge adds private services.
plugin, err := bridge.NewProcess(rpc.ProcessOptions{
    PluginID: "notice-process",
    Command: rpc.Command{Path: "/path/to/child"},
    ChildBoundary: rpc.Boundary{},
    Limits: rpc.Limits{
        Business: rpc.AdmissionLimit{Pending: 16, Handling: 16},
        Control: rpc.AdmissionLimit{Pending: 8, Handling: 8},
        Continuation: rpc.AdmissionLimit{Pending: 8, Handling: 8},
    },
})

// Child: retain the business Unit, including its Config, plugins and instances.
unit, err := bridge.Unit(businessUnit)
// Check err, then pass unit and explicit limits to rpc.ServeUnit:
// rpc.ServeUnit(ctx, rpc.ServeUnitOptions{Identity: identity, Unit: unit, Limits: rpc.Limits{
//     Business: rpc.AdmissionLimit{Pending: 16, Handling: 16},
//     Control: rpc.AdmissionLimit{Pending: 8, Handling: 8},
//     Continuation: rpc.AdmissionLimit{Pending: 8, Handling: 8},
// }})
// Do not install a local core.Plugin in this Unit.
```

Directions are relative to the Child: Publish allows Child dispatch into the Host Bus; Subscribe allows Child handlers. Both sides must declare exactly the same contracts, codec IDs and directions. Sorted declarations and the adapter protocol form the private Binding versions; mismatches fail after Describe and before Initialize. Codec IDs are explicit compatibility promises, not proof that two Go types or codec implementations agree. Each operation also checks the local contract's exact type and wire kind/direction/mode.

Host assembly mounts the local Bus and observers before the process plugin. `MountReady` confirms initial registered owners have reached Host OwnerReady and their committed subscriptions are installed. The loader's AfterReady sends Enable; Child dispatch waits for both its own OwnerReady acknowledgment and Enable. This does not make arbitrary business AfterReady tasks complete before MountReady.

RPC admission fields are unlimited when zero, including when using a Bridge. Neither NewProcess nor Unit installs hidden limits. The assembly above and runnable example write out Business 16/16 and Control/Continuation 8/8 on each peer; these are example capacities, not library recommendations. Integration fixtures declare their own test capacities. Choose values for the workload; setting any admission field to zero removes that cap. Host limits do not configure the Child, and Unit carries no Limits. Unlimited RPC calls remain counted and drained; subscription/token caps below remain independent.

## Ownership and limits

Subscriptions follow pending → committed → active → removed. Commit occurs after the Child saves its handler and cleanup. A dynamic subscription can receive a callback before On returns. Priority, registration order, Global and Topic View routing are decided on the Host; Global never bypasses Topic isolation. Host Once admission is atomic and confirms Child removal even if encoding or delivery fails. A canceled On/Commit cannot return a successful handle after failed rollback.

Cancellation immediately disables local delivery. The `func()` cancellation handle cannot report remote errors: unconfirmed removal fails the current Session and final Scope cleanup retains the error. Owner retirement removes its proxies and cancels admitted work without waiting for that owner's handlers inside the control response. Session stop, initialization failure and process loss revoke all owned subscriptions. Restart creates a new Session; old clients and IDs never target it.

There are at most 1024 pending/committed/active subscriptions per Session and 128 next/base tokens per peer. Expired tokens with running work still consume capacity. IDs increase without reuse; removal returns capacity. RPC Business, Control and Continuation budgets remain independent; Dispatch/Deliver use Business, owner/subscription/Enable/revocation/continuation expiry use Control; Continue uses Continuation. Exhaustion returns `rpc.ErrBackpressure`, without retry or queuing. Custom codecs must be concurrency-safe and encode one valid JSON value. JSONEventCodec rejects unknown fields, duplicate keys, trailing values and null for non-nullable types; pointer/map/slice null remains valid.

`Snapshot()` returns ordinary RPC diagnostics. `EventsSnapshot()` reports Host peer owner/subscription counts, actual Host dispatches, outgoing Deliver calls, Tokens/Continuing, closure and failure. An outgoing Deliver returning is not proof that a canceled Child handler exited. Use RPC actual handler counters, Child node leases/cleanup, and PID/Reaped independently. Stopping still waits for actual Host handlers; a killed Child retains unknown/forced remote cleanup.

Hook selection (`Handle`, `Serial`/`Bail`) and middleware (`Use`, `Waterfall`) use separate input/output codecs:

```go
rpcevents.BindHook(Transform,
    rpcevents.JSONEventCodec[Input]("example/input/1"),
    rpcevents.JSONEventCodec[Output]("example/output/1"),
    rpcevents.Publish|rpcevents.Subscribe)
```

Selection preserves the explicit handled flag, no-handler sentinel and business errors. Waterfall supports input changes, short circuits, result wrapping and nested calls through multiple Children. Each next is single-use and expires when its middleware returns. A bounded control acknowledgment closes Host token admission before the Child waits for next work already admitted; normal expiry does not cancel that work. Cancellation, explicit unsubscribe, owner retirement and Session loss cancel the corresponding tokens. Actual work must exit before its invocation releases leases and token capacity. Known error sentinels survive transport; arbitrary Go error trees do not.

All `*From` variants retain Host Topic/source View routing. A Child source must have been provided by the actual calling Scope and explicitly exported by that Instance in its Unit. Host independently restricts sources to ordinary boundary Exports, restores the exact ServiceSpec, and verifies the loader has provided it. Child-internal services and another owner's exports cannot be used as source addresses. Global bypasses source filtering, while Topic partitioning still applies.

The adapter protocol is `2:<registry-digest>`; rebuild both ends together. See the [bidirectional events example](../examples/events/README.md) for runnable Host and child assembly.

## Source verification

From `rpc/`, create a temporary workspace mapping both source modules:

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go test -race -count=1 -timeout=180s ./events
go vet ./events
```

Before independent publication, pin go.mod to a real Core version containing the new remote cooperation APIs. Tests execute the same race-instrumented test binary as a real Child and require local socket permissions.
