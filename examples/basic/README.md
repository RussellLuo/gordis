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

`NewHost` registers only the two plugin types. The program then mounts the
provider, Alice, and Bob directly through the root Env with `MountReady`. The
Host decodes each instance's `Config` into a fresh plugin object and calls
`Validate` before `Activate`.

See [main.go](main.go) for the complete program,
[isolation](../isolation/README.md) for multiple providers and isolated Envs,
and [ownership](../ownership/README.md) for child lifecycle ownership.
