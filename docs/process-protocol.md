# Process Protocol v1

English | [中文](process-protocol.zh.md)

`process` supervises trusted local child processes and provides bidirectional JSON RPC over stdio. Both the Host and plugin may initiate calls. The application owns business methods, DTOs, and authorization; Go objects, Scopes, and service Keys never cross the process boundary.

The current protocol identifier is `gordis.process/1`, and both peers must match it exactly. An incompatible change requires a new protocol identifier and rebuilt Host, plugin, and manifest artifacts.

## Usage

The Host starts the process and explicitly exports the methods that the plugin may call:

```go
client, err := process.Start(ctx, process.Options{
    Path: executable,
    Identity: identity,
    Instance: "sampler-alpha",
    Generation: 1,
    Handler: hostHandler,
})
```

The plugin receives calls and calls back into the Host over the same session:

```go
err := process.Serve(ctx, os.Stdin, os.Stdout, identity, process.Limits{}, process.Handler{
    Initialize: initialize,
    Call:       pluginHandler,
})
```

`process.Caller.Call(ctx, method, params, result)` is safe for concurrent use. Initialization may call the Host, and a handler may make a nested call to its peer, but applications must avoid recursive calls and wait cycles.

Runnable examples:

- [process](../examples/process/README.md): expose a typed process service to a Gordis consumer.
- [duplex](../examples/duplex/README.md): one nested Host → plugin → Host call.
- [application-plugin](../examples/application-plugin/README.md): a remote Plugin owns its data plane and contributes UI to the Host.

## Wire protocol

- stdio uses UTF-8 JSON Lines; stdout is reserved for the protocol and stderr for logs.
- Every message carries `session`, `instance`, `generation`, and a request ID.
- Each peer allocates its own monotonically increasing request IDs; `cancel` uses the original request ID.
- Deadlines are Unix milliseconds. Cancellation does not guarantee rollback of business side effects and never triggers automatic retry.
- Late messages from another session, instance, or generation are discarded. Duplicate or decreasing request IDs terminate the connection.

Startup proceeds as follows:

```text
hello(gordis.process/1)
  → validate protocol / package / version / contracts
  → initialize(instance, generation, config)
  → ready
```

`hello`, `initialize`, `quiesce`, `dispose`, `shutdown`, and `failed` are reserved methods. Business calls may use only application-defined method names.

Defaults are 64 KiB per message, 16 pending outbound calls, 16 active inbound handlers, and a 16-message write queue. Full queues return `ErrBackpressure`; malformed or oversized messages and I/O failures terminate the session. Applications should use bounded DTOs and context-aware handlers.

## `processbridge`

`processbridge` maps Gordis service contracts to process calls:

- `Bind` registers an explicit client, handler, and DTO encoding for one `Key[T]`.
- The Host-side `Adapter` obtains declared dependencies, starts the process, and publishes a typed proxy under the same Key.
- Child-side `Serve` creates the generation's Scope, injects dependency proxies, and exports the services provided by the business Plugin.

The business Plugin continues to use the ordinary `Spec`, `Start`, `Get`, and `Provide` APIs. A consumer cannot tell whether it received a local object or a process proxy. Wire bindings remain explicit because a service name alone cannot define RPC serialization and failure semantics.

The Adapter ID is the logical plugin type ID and should not encode an execution suffix such as `-process`. The Host assigns the instance ID and generation; the child must not create another Host or renumber the generation.

## Lifecycle and cleanup

With the `gordis.lifecycle/1` contract, shutdown proceeds as:

```text
quiesce → dispose → shutdown
```

| Phase | Meaning |
| --- | --- |
| `quiesce` | Reject new business calls, freeze the remote Scope, and run OnStop |
| `dispose` | Cancel tasks, wait for leases, run Defer in reverse order, and return cleanup results |
| `shutdown` | Stop the transport after disposal completes |
| `failed` | Report a runtime failure for the current session to the Host |

The Host-side Adapter closes its proxy entrance and drains admitted calls before closing the Client. The plugin may still call the Host until remote cleanup completes, so fixed dependencies must not be closed early in OnStop.

Process exit proves OS reclamation, not successful business cleanup:

- `CleanupKnown` says whether the Host received the remote dispose result.
- `Cleanup` records the business cleanup result.
- `Reaped`, `IOComplete`, and `HandlersComplete` separately report process, pipe, and Host-handler reclamation.

On disconnect, crash, or lost cleanup response, the Host treats remote cleanup as unknown and retains a residual rather than starting a new generation over possible live side effects. Killing the child cannot terminate a Host goroutine that ignores cancellation.

## Local packages and boundaries

A manifest contains `id`, `version`, relative `entry`, `sha256`, `protocol`, and `contracts`. The loader checks path containment, executable permission, digest, and contract compatibility. A digest provides integrity, not signing or trust.

The current implementation does not provide remote discovery, network transport, authentication, reconnection, automatic proxy generation, or process-tree cleanup. Run only local programs explicitly supplied and trusted by the application.
