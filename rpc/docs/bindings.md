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
