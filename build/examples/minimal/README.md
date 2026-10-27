# Minimal greeting application

English | [中文](README.zh.md)

Build an English greeting application directly with `gordis-build`, then add
an optional Chinese plugin. This example uses ordinary Gordis plugins and
has no application build wrapper.

## Run

The build module is not published yet; the following installation and build
steps become available after publication. For current source checks, see the
[development guide](../../docs/development.md).

From this example's module directory (`build/examples/minimal`):

```sh
go install github.com/RussellLuo/gordis/build/cmd/gordis-build@latest

gordis-build \
  --app example.com/gordis-minimal/app \
  --replace example.com/gordis-minimal=. \
  --output ./dist/hello
./dist/hello
```

The application prints:

```text
Hello, World!
```

Add the Chinese plugin with `--with`:

```sh
gordis-build \
  --app example.com/gordis-minimal/app \
  --with example.com/gordis-minimal/plugins/chinese \
  --replace example.com/gordis-minimal=. \
  --output ./dist/bilingual
./dist/bilingual --plugin greet/chinese --name Ada
```

The application prints:

```text
你好，Ada！
```

Use a new output path for each build. The binary is delivered alongside a
`.build/` directory containing its build record.

## How it works

`app.Run` includes the English plugin by default. The Chinese package exports
`Plugins()`; selecting it with `--with` adds its plugins to the generated entry.
`--replace` maps only this application's source directory. Gordis core is a
normal dependency in go.mod.

The application registers plugin types through `NewHost`, passes `--name`
into the instance configuration, mounts the chosen plugin and shuts down the
Host. `--check` checks registration without activating plugins.

See [app/run.go](app/run.go) for the application entry,
[custom](../custom/README.md) for application-specific registration descriptions,
and [build records](../../docs/records.md) for delivery details.
