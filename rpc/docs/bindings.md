# Bindings: ordinary interfaces over RPC

English | [中文](bindings.zh.md) · [README](../README.md)

A Binding combines a typed Key, compatibility version and `Codec[T]`. The Codec supplies `Client(Invoker) T`, `Server(T) Handler` and optional `Validate(context.Context, T) error`. Business code uses the ordinary interface; assembly code references the Binding.

## Generate from an ordinary interface

The contract package declares an interface, a `gordis.Key[Interface]` variable and a nonempty string version constant. Generate into a separate companion package in the same module, importing the contract in one direction. Examples use `contract`, `bindings` and the runtime alias `rpc`.

Name the companion package `bindings` and each generated file after its contract: `Greeter` uses `bindings/greeter.go`, and `Provider` uses `bindings/provider.go`. The file header identifies the generator by its full tool path, `github.com/RussellLuo/gordis/rpc/cmd/rpcgen`; the generator still requires an explicit `-out` path.

From `rpc/examples/basic/contract/`:

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key Key -version Version -package bindings -out ../bindings/greeter.go -check
```

`-type`, `-key` and `-version` reference exported Go symbols, avoiding repeated wire IDs and versions. Adopt the same [go:generate directive](../examples/basic/contract/service.go) in your own contract. Your pinned module dependency selects the tool version; the module is currently unpublished.

Commit generated files; ordinary builds do not run the generator. `-check` verifies output without writing. Analysis failures preserve old output; successful generation replaces it atomically. Output must be in a separate package within the contract's module and cannot introduce an import cycle.

Incoming unary and stream handler panics are returned as gRPC `Internal` errors on both the Unit and Host callback sides. Admission counters and leases are released when the handler exits, and later calls remain usable. A panic does not reconstruct an arbitrary business error or roll back side effects.

## Supported methods

```go
// One request, one result; error-only methods are supported too.
Greet(context.Context, string) (string, error)

// One request, synchronous yield calls, then an error result.
Generate(context.Context, Request, func(Chunk) error) error
```

Multiple or zero business arguments, embedded interface methods and exported yield types/aliases are supported. Yield must be the final `func(Chunk) error` argument and is excluded from the request payload. A nil yield fails locally. Local yield errors are returned unchanged and cancel the remote stream. Malformed chunks, early stops, business failures and context cancellation/deadlines have explicit failure paths.

## DTO and compatibility rules

| Item | Rule |
| --- | --- |
| Values | Basic JSON values, exported DTOs, structs, pointers, slices, arrays, string-key maps and recursive DTOs |
| null | Accepted by pointers, slices and maps; rejected by other types, including nested fields |
| Fields | Case-sensitive names; unknown/duplicate fields rejected; omitted fields keep Go defaults |
| Arrays and bytes | Exact array length; byte slices use JSON base64 strings |
| Raw JSON | `json.RawMessage` / `jsontext.Value` and exported aliases embed one valid JSON value, including null; nested fields are owned by the application |
| Envelopes | Requests have exact positional `args`; results/chunks use `value`; error-only success is `{}` |
| Version | `rpcgen.json/1:<Version>`; format prefix and contract version participate in the handshake |

Raw JSON works in parameters, results, nested DTOs and stream chunks. It does not use base64 or convert numbers through `float64`. Its contents are opaque to DTO validation, so application schemas and policies still apply. Envelope and surrounding DTO fields retain their strict checks. Nil RawMessage encodes as null; pointer null follows ordinary Go JSON decoding and becomes a nil pointer. Arbitrary custom JSON/text codecs still require an explicit Codec.

Each generated Binding also exposes a codec factory, such as `bindings.GreeterCodec()`. To retain domain checks before Ready, obtain a fresh codec, set `codec.Validate`, and construct `rpc.NewBinding(contract.Key, bindings.Greeter.Version(), codec)`. This changes neither the default Binding nor its wire format.

Contract authors own semantic compatibility; the format prefix only distinguishes encoding formats. Rebuild Host and child together when adopting API/wire changes during this unpublished stage.

## When to write a Codec

Generic interfaces/DTOs, variadics, arbitrary interfaces, custom JSON/text codecs, and embedded or unexported DTO fields are outside the current generator scope (`json:"-"` can ignore fields). Other callbacks, client streaming, bidirectional streaming and process resources also need explicitly designed adapters.

Use `NewBinding(key, version, Codec[T]{...})` and register unary/stream methods through Handler. An Invoker can access only that Binding's contract; Validate must honor initialization cancellation. Model callbacks as contracts provided by the other side. Represent resources as DTOs/handles with explicit lifetimes. The runtime supplies unary/server-stream transport; Go functions, Scopes and runtime objects cannot be passed directly.

Examples: [generated Greeter](../examples/basic/bindings/greeter.go), [handwritten Binding](../internal/testfixture/service.go), [generated stream and callback contracts](../internal/streamfixture/contract/service.go). Check commands are in [development and verification](development.md).

## Private callbacks and prepared exports for domain adapters

`AttachOptions.LocalImports` accepts private callback values constructed during each Activate with `BindLocalImport(binding, value)`. Each Binding must still match the complete ChildBoundary.Imports, including its Go type and version. Nil, typed nil, duplicate and out-of-boundary entries fail before process acquisition. Attach uses the boundary's Codec/Validate. Uncovered imports still resolve through Scope.Get and require Requires declarations. LocalImport belongs to the current Scope/Session; do not cache activated values in shared definitions or bypass ordinary external service dependencies.

In Accept, call `session.PrepareExports()`, read an endpoint with `PreparedClient(prepared, key)`, then publish the same objects with `prepared.Provide(scope)`. Preparation runs once per Session and caches failures too; reads never rebuild clients. Provide accepts only the originating Scope/generation and permits one publication attempt, including a failed attempt. Return publication errors from Activate so Gordis rolls back every staged service. Stopping rejects preparation and publication. NewProcess uses this same path. The lower-level ExportClient may construct another client on each call; adapters should use PreparedClient to preserve object identity.

Snapshot's `CallbacksClosed` means callback admission is closed; `CallbackHandling` counts actual unary/stream callbacks still executing. `CallbacksDrained` requires both closed admission and zero actual handlers. Child CleanupReport and Reaped are separate evidence. Fixed import cleanup callbacks remain available through Dispose. Scope final cleanup and its fixed dependencies remain pending until actual callbacks exit, even if a caller has already timed out.

## Immediate admission

Each side of each Session has independent outbound Pending and inbound Handling budgets. Business, Control and Continuation each use `AdmissionLimit{Pending, Handling}`: every zero field means unlimited, positive values set explicit caps, and negative values fail validation. The zero-value Limits imposes no application concurrency cap. Unlimited work is still counted, rejected after admission closes, and drained during shutdown. There is no queue, automatic retry or borrowing between classes. Message-size and timeout defaults are separate and unchanged.

Choose numbers explicitly for the workload; the library supplies no numeric admission preset. For example, if a particular receiver's capacity has been measured at 32 handlers, its local configuration can be:

```go
limits := rpc.Limits{
    Business: rpc.AdmissionLimit{
        Pending:  0,  // Unlimited outbound Business calls.
        Handling: 32, // Up to 32 actual inbound Business handlers on this peer.
    },
}
```

Here Control/Continuation remain unlimited. This example does not imply that 32 is appropriate for another service. No defaults refill zero admission fields.

Ordinary generated unary and stream methods use Business. A handwritten Codec can declare `MethodClasses: map[string]rpc.AdmissionClass{"Commit": rpc.Control, "Continue": rpc.Continuation}`. NewBinding copies the map. Unlisted methods use Business; invalid classes, class entries for missing handlers, and non-Business streams fail validation. Both peers use their own Binding declarations; the request carries no class. Changing a method's class requires a new Binding version. Control handlers must perform bounded state operations without user handlers or business drain waits. Continuation executes an existing invocation's next/base with a separate budget.

Pending is acquired before payload serialization and held through the actual Call/Stream return, including stream yields and result decoding. Handling is held until the actual unary/stream handler returns. Caller cancellation releases Pending when the caller returns; it cannot release a still-running remote handler's Handling. Pre-canceled calls do not serialize or enter handlers. Streams and unary methods share the Business budget.

Exhaustion fails immediately with `errors.Is(err, rpc.ErrBackpressure)` and gRPC `ResourceExhausted`; message-size exhaustion uses the same mapping. Initialize, Quiesce, Dispose and Watch bypass domain budgets. Session shutdown closes outbound admission; fixed import callbacks remain available through Child local cleanup. After local cleanup, the Child closes its callback connection before waiting for remaining outbound work, avoiding a drain/connection-close cycle. Host final cleanup still waits for actual callbacks and any blocked serialization to exit.

Host and child configure their own Limits; changing Host limits does not change the child's limits. `Snapshot().Admission` reports this Host side's three Pending/Handling pairs. `CallbackHandling == 0` means no callbacks are currently executing; `CallbacksDrained` additionally requires closed callback admission. `CallbackTransportClosed` records completed callback transport shutdown. These are separate from Child CleanupReport, Reaped and blocked outbound serialization. A hung serializer or handler keeps its count and Scope cleanup responsibility until it actually returns.

Optional adapters can use `prepared.ProvideServices(scope, services...)` to publish a declared subset and keep other prepared clients Session-private. It shares the one publication attempt with Provide, including failure; duplicate or undeclared selections are rejected. `Unit.Validate()` exposes the same closed-assembly checks used by ServeUnit without configuration or activation.

### Choosing capacities

Limits count concurrent work, not requests per second or a worker-pool size. For each peer, class and direction, all methods/contracts share one count; sessions do not share quotas. There is no per-method fairness. Observe simultaneous calls, handler duration, stream lifetime, memory/CPU and dependent-service capacity under the actual workload before choosing caps. Pending and Handling need not be equal: they protect different work on different peers.

| Field | What to account for |
| --- | --- |
| Business | Ordinary calls and reverse callbacks; slow services and long-lived streams compete with short calls in the same Session. A stream occupies one slot throughout its lifetime, not per message. |
| Control | Short state operations only. Keep these independent of user business execution; do not classify a slow business method as Control to evade saturation. |
| Continuation | Adapter-validated next/base execution for accepted invocations. Allow for simultaneous and nested continuations; finite quotas cannot guarantee arbitrary nesting. Ordinary callbacks do not automatically enter this class. |
| Pending | Includes serialization, transport waiting, response decoding and stream yields until the actual caller returns. Canceling a blocked serializer/yield does not release its slot. |
| Handling | Actual handler execution until it returns. A canceled caller can return while its handler still occupies capacity, causing subsequent calls to be rejected. |

On the Host, Pending counts calls to Child exports and Handling counts Child callbacks to Host imports; on the Child the directions are reversed. Increasing Host Pending does not raise Child Handling. RPC's Initialize/Quiesce/Dispose/Watch bypass these budgets; ordinary cleanup service callbacks use their declared class and have no reserved capacity. Callback reentry can require another slot while outer calls still hold theirs, so low finite Business limits can reject nested work. The Continuation class protects only explicitly classified existing-invocation work.

`errors.Is(err, rpc.ErrBackpressure)` also covers message-size exhaustion. An error from a nested call or an oversized response can follow earlier business effects, so this sentinel is not proof that the entire operation never ran or can safely be retried. RPC does not automatically retry.

The remaining Limits fields use different zero-value rules:

| Field | Scope and default |
| --- | --- |
| MaxMessage | Send/receive limit on local RPC servers and the Host outbound connection; bytes per gRPC message including encoding/envelope overhead, 4 MiB when <= 0. Not a total stream/session memory limit. This field is not applied to the Child broker callback client; that connection uses go-plugin's client defaults and remains subject to the Host callback server's limits. |
| StartupTimeout | Host discovery/Attach startup including import validation, Initialize and Accept; 5s when <= 0. An earlier context deadline wins; custom validators/Accept must cooperate with cancellation. No business-call deadline is supplied. |
| StopTimeout | Host stop-control waits; 4s when <= 0. OnStop Quiesce and final Quiesce/Dispose each receive a timeout. This does not bound total actual cleanup; blocked callbacks/serializers can outlive Child reaping. Bound the caller's Shutdown wait separately. |
| OrphanGrace | Child cleanup grace after parent/main-connection loss or ServeUnit context cancellation; 2s when <= 0, then stop serving even if cleanup is incomplete. Does not bound normal Host Dispose or establish successful cleanup. |

`Snapshot().Admission` contains observed counts on the Host, not configured capacities: a zero count means idle. Keep these separate from the other peer's work, Child cleanup reports and Reaped.
