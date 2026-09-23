# Typed services and configuration

This example extends the README greeter into two plugin types. One `greeter`
instance provides a typed `Greeter` service; two `greeting` instances consume
it with different JSON configurations.

## Run

From the repository root:

```sh
go run ./examples/basic
```

Expected output:

```text
alice: Hello, Alice
bob: Hello, Bob
```

## How it works

`greeterKey` defines the shared contract. The provider declares it in
`Provides` and publishes a value with `gordis.Provide`; each consumer declares
it in `Requires` and reads its fixed binding with `gordis.Get`.

Both consumers use the same `greetingPlugin` type. The Host decodes each
instance's `Config` into a fresh plugin object, calls `Validate`, and starts the
provider before its consumers.

See [main.go](main.go) for the complete program and
[composition](../composition/README.md) for multiple providers, service slots,
and lifecycle ownership.
