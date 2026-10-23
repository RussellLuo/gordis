# Architecture and lifecycle

English | [中文](architecture.zh.md) · [README](../README.md)

gordis-rpc runs a Gordis assembly in a child process and connects services through explicit Bindings. Business objects, Scopes and Go functions stay in their own processes; DTOs cross the boundary.

## Responsibilities

```mermaid
flowchart LR
    subgraph Host[Host process]
        C[Ordinary consumer] --> P[NewProcess / typed client]
        H[Host service / callback handler]
    end
    subgraph Child[Child process]
        S[ServeUnit / export handler] --> U[Ordinary Plugins in Unit]
        I[Import client]
    end
    P -->|Export calls · gRPC| S
    U -->|Uses Imports| I
    I -->|Callbacks · Broker gRPC| H
```

| Layer | Owns |
| --- | --- |
| Ordinary Gordis Plugin | Spec/Activate, Get/Provide, business configuration and cleanup |
| Domain adapter / Binding | DTOs, serialization, typed clients, method handlers and domain contributions |
| gordis-rpc | Program configuration declaration, discovery, strict decoding and instance mapping; Unit assembly, boundary checks, Session, readiness and cleanup coordination |
| go-plugin / gRPC | Process startup, base handshake, connections, Broker and RPC transport |

The runtime imports neither Qi nor legacy `gordis/process` or `processbridge`. Tool, model and prompt rules belong to their domain adapters. Programs are trusted and local; process isolation is not an untrusted-code sandbox.

The jsonschema-go fork generates and resolves configuration Schema and validates input. gordis-rpc adapts those operations without exposing third-party types in its public API. Qi's tool adapter passes `ServerOptions.Config` to Unit and offers `ConfigForTool(value)` to hide its private tool instance ID. The runtime has no dependency on that adapter. See [configuration declarations](concepts.md#declare-program-configuration) for author rules.

## Startup: business readiness follows process startup

1. **Declare:** the Host registers `NewProcess(ProcessOptions)`; the child declares Identity and Unit through `ServeUnit`.
2. **Resolve dependencies:** activation resolves Imports through Host Scope.Get, registers teardown, then starts the program. StartupTimeout begins before import validation.
3. **Discover and check:** Describe returns identity, the complete Boundary and ConfigSchema. The Host compiles this process's Schema and validates program configuration before installing callbacks or sending Initialize. The Host compares contract names, versions and directions against ChildBoundary and checks identity constraints before constructing or activating business instances.
4. **Initialize:** Imports require a Broker callback server. Initialize sends Session and business configuration. After Unit declaration validation, the child independently validates input, calls New, strictly decodes configuration and calls Configure. All mappings are staged on detached instance copies before Gordis preparation or activation. The child assembles those instances through a Gordis ChangeSet, waits for Ready, then resolves, validates and fixes exports.
5. **Publish and watch:** `NewProcess` constructs and checks all export clients once, then publishes those same instances through Scope.Provide. Attach runs optional Accept after remote Ready and installs a Scope-owned Watch.

Initialization, client preparation, publication or Accept failures trigger idempotent cleanup before returning; Gordis rolls back staged Scope services. An established connection or live PID does not establish business readiness.

Unit declaration validation, including unique instance IDs, precedes configuration methods. Each initialization calls New and Configure at most once, without retries. Before serializing any mapping value, the child checks all target IDs; serialization or cancellation failures discard the detached instance copies and prevent business activation. Inputs, Schema and instance JSON are copied at their boundaries. Ordinary Plugin decoding and Validate still run after mapping. Schema/decode diagnostics omit input values; authors control the contents of Configure errors.

## Choosing an entry point

`DescribeProgram(ctx, command, limits)` acquires and reaps a real process to inspect identity, child-relative Contracts and ConfigSchema. It installs no callbacks and runs no initialization or business cleanup.

Discovery reaps on success, error and cancellation. It requires no Scope and returns detached data. Plugins, Instances and internal configuration targets are not transmitted. Attach always acquires and checks its own description, including Schema, even if the Host probed the same command earlier.

For ordinary services, use `NewProcess` to handle Requires/Provides and proxy publication. Use `Attach(ctx, scope, options)` and Accept directly for domain contribution registration or similar adapter logic.

A Plugin using Attach directly must include `RequiredServices(options.ChildBoundary.Imports)` in Requires. Attach resolves dependencies from Scope, preserving callback provider lifetimes; it does not accept arbitrary Host service objects. It does not publish Exports automatically: obtain proxies with `ExportClient(session, key)`, then explicitly Provide them or register domain contributions.

## Calls and cancellation

Each Attach generates a random Session ID tied to the Host instance and generation. An Invoker is restricted to one contract and Session; ExportClient accepts only declared exports with a matching Go type.

Export requests hold a Scope Ready lease, preventing service release while the call is active. Cancellation reaches the handler; the lease is released only when the actual handler returns. Server streams send chunks synchronously without a runtime production queue. Stopping consumption cancels the remote stream. Domain layers own business concurrency budgets.

The protocol is `gordis-rpc/1`. Control messages and domain DTOs use JSON carried by protobuf BytesValue over gRPC unary/server-stream calls. The transport handshake remains version 1. Rebuild Host and child together; older configuration payloads are not compatible. Wire Go types and RPC paths are private. See [Bindings](bindings.md) for the generated payload format.

## Shutdown and failure

Normal shutdown proceeds through **Quiesce to close admission and initiate child Shutdown → Dispose to await the cleanup report → Host process reaping → Host callback server shutdown**. The callback server waits for actual handler exit, keeping dependency providers alive until callbacks finish.

Watch reports child node failures and unexpected termination to the Host. If the main connection closes or parent identity changes, the child attempts cleanup and stops serving within OrphanGrace, allowing the standard main to return. Parent checks also cover startup before Initialize. Uncooperative business code or callbacks can leave cleanup incomplete.

`Session.Snapshot()` and `ProcessPlugin.Snapshot()` return independent diagnostic copies:

| Field | Meaning |
| --- | --- |
| Ready | Remote readiness was reached; cleared on normal reaping, not a live health check |
| Nodes | Initialization-time snapshots, not continuously updated node state |
| Reaped | The process was reaped; this does not prove business cleanup |
| Cleanup | Inspect CleanupComplete, Forced and Errors for actual cleanup results |

ProcessPlugin retains failed-Attach diagnostics and returns a zero value before Attach. Source entry points: [config.go](../config.go), [program.go](../program.go), [process.go](../process.go), [session.go](../session.go), [unit.go](../unit.go), [server.go](../server.go).
