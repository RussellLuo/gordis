# Core concepts

English | [中文](concepts.zh.md) · [README](../README.md)

Consider Greeter: a business Plugin implements `Greeter`, and a consumer calls it through `scope.Get(contract.Key)`. Moving the provider into a child process gives the consumer a proxy implementing the same interface. Contracts and business Plugins keep using ordinary Gordis APIs.

## From interface to process

| Concept | Question it answers | Greeter equivalent |
| --- | --- | --- |
| Contract | What service can I call? | The `Greeter` interface, typed Key and contract version |
| Binding | How does that contract cross a process boundary? | Typed client, method handlers and version bound to the Key |
| Boundary | Which services cross the program boundary? | Child Imports and Exports |
| Unit | What runs and how is it assembled inside the child? | Plugins, instances, program configuration, imports and sourced exports |
| Session | Which running instance does a call belong to? | Session ID, Host instance/generation and diagnostics |

Contracts and business Plugins need no RPC imports. Generated or handwritten Bindings live in adapter packages, referenced explicitly by Host and child assembly code. See the [basic layout](../examples/basic/README.md).

## Imports and Exports are relative to the child

- **Imports** are services the child needs from the Host, such as logging or callbacks. The Host resolves them through Scope.Get and exposes a callback channel.
- **Exports** are services the child provides to the Host, such as Greeter. The Host obtains typed clients for them.

Consequently, `NewProcess` maps Imports to the Host Plugin's Requires and Exports to Provides. Both sides check the complete set of contract names, versions and directions, even if some services are never called.

The Host declares the expected child boundary in `ProcessOptions.ChildBoundary` or `AttachOptions.ChildBoundary`. The child declares its actual boundary through `Unit.Imports` and `Unit.Exports`, using the same directions. `Unit.Boundary()` retains Imports and takes each Export's Binding, omitting its internal source Instance, to produce the contract that must match the Host declaration.

Without Imports, no callback server is needed. Units with no exports, only imports, or no services are valid too.

## A Unit is a closed static assembly

`Unit.Plugins` registers ordinary Plugins; `Instances` selects running instances. `Config` declares program configuration through `NewConfig[Config]()`: Config.New supplies defaults and Configure(ctx) maps them to local instances. Each Export names its source `Instance` and Binding.

Every Requires must be satisfied by a Unit instance or an Import. Each service name has a single provider; multiple-provider View routing within a Unit is not supported. Gordis owns dependency ordering, cycle detection, activation and rollback.

Imports and Exports describe only the cross-process surface; services and dependencies between local Plugins need no entry there.

A Unit may contain several Plugins, instances and exports; it need not correspond to one business Plugin.

## Program identity and service contracts have separate names

`Identity.ID` identifies the program, `Identity.Version` its version, and `Identity.Protocol` the substrate protocol `rpc.Protocol`. A service Key's name and Binding.Version identify a contract and its compatibility version.

One program can provide several contracts, so its ID need not match a contract ID. `ProcessOptions.PluginID` separately names the loader registered with the Host. Basic declares program identity and each side's boundary directly in the [Host](../examples/basic/host/cmd/main.go) and [plugin program](../examples/basic/plugin/cmd/main.go) entries. Ordinary interfaces, Keys and contract versions live in [contract](../examples/basic/contract/service.go).

A nonzero Host Identity pins the expected identity. A zero value permits discovery through Describe after startup; ChildBoundary must still match exactly.

## Configuration and running instances

`NewProcess` constructs a registrable Plugin prototype without starting a program. Each `Spec.New` creates independent state; activation starts its own child process and Session.

Instance Config is a program JSON object, such as `{"prefix":"Welcome"}`. The Host validates it against the current Describe Schema before Initialize. The child validates independently, then calls New, strictly decodes the original input and calls Configure. Mapped entries fully replace local instance configuration; other instances retain presets. Ordinary Plugins still decode and Validate their own configuration. A zero `Unit.Config` accepts only omitted input or an empty object; the Host needs no configuration mode.

`DescribeProgram(ctx, command, limits)` executes the program, returns its identity, child-relative Contracts and ConfigSchema, then reaps the process. It runs no initialization, config methods or business cleanup. Required fields must be supplied explicitly; New cannot satisfy them.

Program path, arguments, environment, identity, ChildBoundary and Limits come from ProcessOptions, separately from business configuration.

## Declare program configuration

Define a named, non-pointer struct in the program assembly package, with value-receiver methods. This is the declaration used by [basic](../examples/basic/plugin/cmd/main.go):

```go
type Config struct {
    Prefix string `json:"prefix,omitempty" jsonschema:"Greeting prefix" jsonschemax:"minLength=1"`
}

func (Config) New() Config { return Config{Prefix: "Hello"} }
func (c Config) Configure(context.Context) (map[string]any, error) {
    return map[string]any{"greeter": c}, nil
}
```

Call `config, err := rpc.NewConfig[Config]()`, handle the error, then set `Unit.Config = config` before `ServeUnit`. NewConfig generates and compiles the Schema without calling New or Configure. No source scan, configuration generator or extra Schema file is needed. Put cross-field business checks at the start of Configure; the program configuration API has no separate Validate hook. Ordinary business Plugins retain their own configuration and Validate methods.

`json` controls field names, exclusion and optionality; `jsonschema` supplies descriptions, and `jsonschemax` adds validation constraints. For supported types, tag syntax and constraints, see [jsonschema-go: Validation tags](https://pkg.go.dev/github.com/RussellLuo/jsonschema-go/jsonschema#hdr-Validation_tags).

Required fields are checked against input before New runs. Omitted fields retain New's defaults; explicit zero values override them, and null follows Schema and encoding/json rules. Defaults are not inferred into the Schema or automatically validated: authors must test them. New must return independent mutable defaults on every call. New and Configure perform short computations without acquiring resources; Configure must respond to context cancellation.

Configure's keys are child-local instance IDs. One program configuration can map to several instances, using different internal configuration structs. All targets must exist; every value must serialize to a JSON object. Nil or an empty map means no overrides. An entry replaces the entire preset instance Config; it does not merge with that preset. When passing explicit zero values internally, use a struct without omitempty/omitzero on those fields, or a map. Keep internal instance IDs and types out of the Host's configuration contract.

Input must be one JSON object; omitted input becomes `{}`. Root null, arrays, scalars, trailing values and unknown fields fail. Schema validation uses float64 numbers; strict Go decoding uses the original bytes, preserving their numeric representation and rejecting Go overflow or incompatible literals. Schema bounds and enumerations beyond about ±2^53 may lose precision. Authors must ensure custom JSON representations match the inferred Schema.

The Host accepts an explicit object-root Schema and uses the library's Resolve/Validate behavior, currently defaulting to Draft 2020-12. Explicit dialects and local references follow library rules; gordis-rpc adds no version whitelist. Without an external loader it cannot fetch network or file references; this is not full standard meta-schema validation. The Schema travels with the program description; Plugins and Instances stay private. A prior DescribeProgram probe is informational: Attach always checks its own process's description again. Manage Schema and validation changes with the program identity version, including changes from dependency upgrades.

Continue with [architecture and lifecycle](architecture.md) for readiness and failure handling.
