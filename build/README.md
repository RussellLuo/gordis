# Gordis build

English | [中文](README.zh.md)

Gordis build compiles selected Go application and plugin packages into a standalone
executable. It handles dependency resolution, entry generation, compilation,
application checks and delivery; the application owns configuration and runtime
lifecycle.

The independent module is `github.com/RussellLuo/gordis/build`. It has not been
published yet; current source checks are described in the [development guide](docs/development.md).

## Getting started

| Approach | Start here |
| --- | --- |
| Use `gordis-build` to assemble ordinary Plugin applications | [Minimal](examples/minimal/README.md): build an English greeting, then add a Chinese plugin. |
| Call `Build` with your own entry generator and assembly check | [Custom](examples/custom/README.md): preserve plugin registration descriptions and default names. |

Applications with their own build command can also use `Run` to share standard
argument parsing and the same build engine.

## Build output

A successful build delivers the executable and an adjacent `<output>.build/`
directory with dependency records, generated source and logs. Checks run before
delivery, and existing output paths are refused. The executable runs without Go,
the build library or the record directory.

## Continue learning

- [Command and SDK usage](docs/usage.md): CLI conventions, requests and adapters.
- [Build records](docs/records.md): record contents and custom encoding.
- [Development checks](docs/development.md): independent modules and local source verification.
