# Basic remote Service

English | [中文](README.zh.md)

Run an ordinary, configurable Greeter Plugin in a child process. The Host's
consumer uses `scope.Get(contract.Key)` and calls `Greet("Gordis")` through the
same interface as a local provider.

## Run

From `rpc/`:

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
```

Expected output:

```text
Hello, Gordis!
```

The Host validates program JSON against the discovered Schema without importing the program's configuration type:

```sh
/tmp/gordis-rpc-basic-host -config '{"prefix":"Welcome"}' /tmp/gordis-rpc-basic-plugin
# Welcome, Gordis!
/tmp/gordis-rpc-basic-host -config '{}' /tmp/gordis-rpc-basic-plugin
# Hello, Gordis! (Config.New's default)
/tmp/gordis-rpc-basic-host -config '{"prefix":""}' /tmp/gordis-rpc-basic-plugin
# Fails validation and exits with status 1; no greeting is printed.
/tmp/gordis-rpc-basic-host -config '{"unexpected":true}' /tmp/gordis-rpc-basic-plugin
# Also fails on the Host before Initialize, with status 1 and no greeting.
```

## Ordinary code and program assembly

Both sides use the same layout: `plugin.go` defines an ordinary business Plugin,
and `cmd/main.go` assembles the program.

```text
basic/
  host/plugin.go       # Consumer Plugin
  host/cmd/main.go     # Host program entry
  plugin/plugin.go     # Provider Plugin
  plugin/cmd/main.go   # Plugin program entry
  contract/service.go  # Ordinary interface, Key and version
  bindings/greeter.go   # Generated RPC Binding
```

The [contract](contract/service.go) declares `Greeter`, its typed Key, semantic
Version and logical Plugin ID. The [business Plugin](plugin/plugin.go)
provides that Key with a default prefix of `Hello`; Gordis decodes and validates
its configuration. The [consumer Plugin](host/plugin.go) gets the Key and calls the ordinary
interface. Contract and business code have no RPC dependency.

`contract.PluginID` is `example/basic/greeter-provider`; the service Key is named
`example/basic/greeter`. They are declared independently: the former selects a
plugin implementation, the latter identifies a service contract, and
`Spec.Provides` connects them.

The consumer declares `PluginID = "example/basic/greeter-consumer"` in the `host`
package, shared by its `Spec()` and the Host program entry. It depends on Greeter
through `Requires`. Instance IDs retain short names such as `"consumer"` and `"greeter"`.

The [Host entry](host/cmd/main.go) creates `rpc.NewProcess` with the executable path,
expected program identity and ChildBoundary declared inline. The plugin program declares the
same identity in its entry; no shared declaration package is needed. The library forwards
instance configuration and publishes the generated Greeter proxy after remote
Ready. Starting the executable alone does not make the business Ready: the plugin program's
Unit must activate and resolve its export first.

The [plugin program entry](plugin/cmd/main.go) assembles one ordinary Greeter instance,
`Config: config` from `rpc.NewConfig[Config]()`, no Imports, and one generated Export sourced from
that instance, then calls `ServeUnit`. Config.New supplies Hello; Configure maps
the program configuration to the internal greeter instance. The CLI defaults to
`{}`; empty prefixes and unknown fields fail on the Host before Initialize. No Host callback service is needed. The
Host receives the greeting, completes Shutdown and joins business/cleanup errors.

The program struct is named and non-pointer, with value-receiver New/Configure
methods; its json tag makes prefix optional, jsonschema describes it, and
jsonschemax enforces minLength. Required input is validated before defaults;
Configure maps by the child-local instance ID, which the Host need not know.
See [program configuration](../../docs/concepts.md#declare-program-configuration)
for mapping, zero values and Schema limits.

## Generate the Binding

The contract owns one `go:generate` directive. After editing the interface, run
from `rpc/examples/basic/contract/`:

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key Key -version Version -package bindings -out ../bindings/greeter.go -check
```

Commit [bindings/greeter.go](bindings/greeter.go) with the contract;
builds use it directly and do not run the generator. The companion package
contains the generated client, handler and Binding. It imports ordinary APIs as
`contract` and the runtime as `rpc`; implementation names are `greeterClient`
and `newGreeterHandler`. Authors maintain ordinary
code and program assembly without hand-writing RPC adapters.

The Binding version is `rpcgen.json/1:1`: strict positional `args` requests and
`value` responses. When changing the contract, update the generated Bindings
and rebuild both Host and plugin programs.

## Verify

From `rpc/`:

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

Tests build real Host and plugin programs with `-race`, compare Local/process results
using the same ordinary test consumer, and observe cleanup/reaping after success
and rejected configuration. Dependency audits keep provider business code out of
the Host and RPC runtime/generator code out of both business packages. Generic loader
and wire failure tests belong to the library and generator suites.
