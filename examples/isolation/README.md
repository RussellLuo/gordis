# Isolated service instances

English | [中文](README.zh.md)

This example connects two consumers to different providers of the same typed
service.

```text
store-main  ── store.main  ──> collector-main
store-audit ── store.audit ──> collector-audit
```

## Run

From the repository root:

```sh
go run ./examples/isolation
```

Expected output:

```text
collector-main -> store-main
collector-audit -> store-audit
```

## How it works

`Env.Isolate` maps `storeKey` to `store.main` and `store.audit`. Providers and
consumers mounted through the same Env use the same label, so each collector
receives the intended store without adding deployment labels to either plugin.

All four instances are independent root instances. Isolation changes service
visibility; it does not create lifecycle ownership.

See [main.go](main.go) for the complete program.
