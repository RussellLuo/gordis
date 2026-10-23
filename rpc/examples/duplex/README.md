# Imported dependencies and nested callbacks

English | [中文](README.zh.md)

Building on [basic](../basic/README.md), the plugin program's Greeter calls a
UserDirectory provided by the Host. Here duplex means service calls in both
directions, not gRPC bidirectional streaming.

```text
Host consumer → plugin program Greeter.Greet("user-42")
              → Host UserDirectory.DisplayName("user-42")
              ← "Gordis"
              ← "Hello, Gordis!"
```

## Run

From `rpc/`:

```sh
GOWORK=off go build -o /tmp/gordis-rpc-duplex-host ./examples/duplex/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-duplex-plugin ./examples/duplex/plugin/cmd
/tmp/gordis-rpc-duplex-host /tmp/gordis-rpc-duplex-plugin
```

Expected business output:

```text
callback: user-42 -> Gordis
Hello, Gordis!
```

The Host Directory prints the callback line; the consumer prints the greeting
after Greeter returns. The CLI takes only the plugin executable path. There is
no business configuration.

## One Import, one Export

- The [Greeter contract](contract/greeter.go) and [Directory contract](contract/directory.go)
  contain ordinary Go interfaces, Keys, a version and generation directives.
- The [Host Directory](host/directory.go) is an ordinary provider. The
  [Greeter Plugin](plugin/plugin.go) requires Directory, obtains its fixed dependency
  through `scope.Get`, and passes the call context to it from `Greet`.
- The [Host consumer](host/plugin.go) only gets Greeter and calls its ordinary
  interface. Business packages and contracts have no RPC dependency.
- The [Host entry](host/cmd/main.go) mounts Directory, `rpc.NewProcess` and the
  consumer in that order, waiting for each to become Ready. The loader maps Imports
  to Requires, keeping Directory alive until actual callbacks and process cleanup finish.
- The [plugin program entry](plugin/cmd/main.go) uses `ServeUnit` to assemble one
  Greeter instance, importing `bindings.UserDirectory` and exporting
  `bindings.Greeter` from that instance.

The Host's `ChildBoundary` corresponds to the plugin program's `Unit.Boundary()`.
**Imports/Exports always refer to the plugin program**, with the same direction
on both sides: Directory is an Import and Greeter is an Export. The Unit uses a zero
ConfigSpec, accepting only omitted configuration or an empty object. Program entries own
identity, boundaries and lifecycle; the Host does not import Greeter's business implementation.

No NewConfig or Configure method is needed here. The zero declaration's Schema
is `{"type":"object","additionalProperties":false}`; nonempty configuration
fails validation before Initialize. See [configuration rules](../../docs/concepts.md#declare-program-configuration).

Plugin registration IDs and service Keys are declared independently and connected
through `Spec.Provides`:

| PluginID (implementation) | Key name (service contract) |
| --- | --- |
| `example/duplex/greeter-provider` | `example/duplex/greeter` |
| `example/duplex/directory-provider` | `example/duplex/directory` |

The consumer declares `PluginID = "example/duplex/greeter-consumer"` in the `host`
package, shared by its `Spec()` and the Host program entry. It depends on Greeter
through `Requires`. Instance IDs retain short names such as `"consumer"`, `"greeter"`
and `"directory"`.

## Generate and verify

From `rpc/examples/duplex/contract/`:

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key GreeterKey -version Version -package bindings -out ../bindings/greeter.go -check
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type UserDirectory -key DirectoryKey -version Version -package bindings -out ../bindings/directory.go -check
```

Both generated Bindings are committed; ordinary builds do not run generation.
From `rpc/`, verify with:

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

Example tests build real Host/plugin programs with `-race`, checking both output
lines, successful exit and production dependency boundaries. Concurrent callbacks,
initialization/cleanup callbacks, cancellation and actual handler exit belong to
library tests; see [Architecture and lifecycle](../../docs/architecture.md).
