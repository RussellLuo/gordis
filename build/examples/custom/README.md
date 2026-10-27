# Custom greeting application

English | [中文](README.zh.md)

This example calls `Build` directly and keeps application-specific registration
information. Each greeting plugin supplies a default name: `World` for English
and `朋友` for Chinese. Runtime arguments can override either default.

## Run

The build module is not published yet. After publication, use `go get` below
to replace its development placeholder. For current source checks, see the
[development guide](../../docs/development.md).

From this example's module directory (`build/examples/custom`):

```sh
go get github.com/RussellLuo/gordis/build@latest
go run ./cmd/build --output ./dist/greeting
```

Run the application with defaults or an explicit name:

```sh
./dist/greeting
./dist/greeting --plugin greet/chinese
./dist/greeting --name Ada
./dist/greeting --plugin greet/chinese --name Ada
```

Expected output, in order:

```text
Hello, World!
你好，朋友！
Hello, Ada!
你好，Ada！
```

Use a new output path for each build. `./dist/greeting --verify-assembly`
checks assembly without activating plugins or printing a greeting.

## How it works

Each plugin package exports `Registration() app.Registration`, containing a
Gordis prototype and a `DefaultName`. `app.Compose` checks the descriptions and
registers prototypes; `app.Run` writes the selected default or explicit name
into the instance configuration before mounting the plugin.

The build recipe supplies Generate and Validate to `Build`. Generate emits
calls to `Registration`, `Compose` and `Run`; Validate runs `--verify-assembly`.
Only application source is mapped locally. Core/build use normal go.mod
dependencies, and the runtime program does not depend on the build library.

See [buildconfig/adapter.go](buildconfig/adapter.go) for the build recipe and
[app/registration.go](app/registration.go) for the application's registration
model. For the native command, see [minimal](../minimal/README.md);
for delivery details, see [build records](../../docs/records.md).
