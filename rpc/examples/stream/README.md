# Streaming results and early stop

English | [中文](README.zh.md)

After [basic](../basic/README.md) and [duplex](../duplex/README.md), this example
adds server-streaming: one request produces a finite sequence of typed chunks.
It uses a deterministic Generator with no model SDK or business configuration.

## Run

From `rpc/`:

```sh
GOWORK=off go build -o /tmp/gordis-rpc-stream-host ./examples/stream/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-stream-plugin ./examples/stream/plugin/cmd
/tmp/gordis-rpc-stream-host /tmp/gordis-rpc-stream-plugin
```

Expected business output:

```text
complete: 1
complete: 2
complete: 3
partial: 1
partial: 2
consumer stopped
```

The CLI takes only the plugin executable path. The Host makes two calls: fully
consume `count=3`, then request `count=5` and stop consuming after the second chunk.
Only the expected local sentinel counts as a successful early stop; other errors
fail the program. Shutdown errors also fail the program.

## An ordinary streaming interface

The [contract](contract/generator.go) declares:

```go
type Generator interface {
    Generate(context.Context, int, func(Chunk) error) error
}
```

Positive count yields indices 1 through count, zero yields nothing, and a negative
count returns a business error. The [provider](plugin/plugin.go) checks context
and yields synchronously, returning yield errors immediately. It creates no
background producer or application queue. The [consumer](host/plugin.go) obtains
Generator through `scope.Get`; both business packages use only the ordinary contract.

**yield is a local function on each side; it is never serialized.** The generated
[Binding](bindings/generator.go) encodes chunks on the server and decodes them
before calling the Host's local yield. Returning an error from that yield preserves
the local error and cancels the remote stream; the sentinel is not reconstructed
from a remote Go error object.

The [Host entry](host/cmd/main.go) uses the same `rpc.NewProcess` loader as basic.
The [plugin program entry](plugin/cmd/main.go) assembles one Generator instance
through `ServeUnit`: no Imports, one Export. Its zero `ConfigSpec` accepts omitted
configuration or an empty object. The entries own identity, boundaries and lifecycle;
the Host does not import the provider implementation.

Early return by the client does not prove that the actual remote handler has
exited. Transport buffering also means the server may have produced more than two
chunks before cancellation arrives. This example does not measure backpressure
or establish a byte-level memory bound. See [Architecture and lifecycle](../../docs/architecture.md)
and [Bindings](../../docs/bindings.md) for the library boundaries.

## Generate and verify

From `rpc/examples/stream/contract/`:

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Generator -key GeneratorKey -version Version -package bindings -out ../bindings/generator.go -check
```

The generated Binding is committed; ordinary builds do not run generation.
From `rpc/`:

```sh
GOWORK=off go test -race -count=1 ./examples/...
GOWORK=off go vet ./examples/...
```

Example tests build real Host/plugin programs with `-race`, checking complete and
partial output, successful exit and production dependency boundaries. Cancellation,
deadlines, partial failures, concurrent isolation, actual handler exit and cleanup
belong to library tests, using dedicated fixtures instead of example fault switches.
