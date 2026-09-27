# Ownership and readiness

English | [中文](README.zh.md)

This example separates lifecycle ownership from Service readiness:

- `collector.Env().Mount` makes `metrics` a child, so it is reclaimed with that
  exact collector generation.
- `metrics` requires `MetricsBackend`; that Service dependency, rather than
  its parent relationship, decides when it can activate.
- The missing backend leaves `metrics` pending without preventing `collector`
  from becoming ready.

Run it from the repository root:

```bash
go run ./examples/readiness
```

Expected output:

```text
collector ready; metrics pending
metrics ready: backend=prometheus
```

The main flow mounts the child from the ready collector's Env, reports the
intentional `pending` state, and then mounts the backend. Gordis activates the
child automatically, and its `Activate` method prints the backend it received.
No diagnostic polling is needed. See [main.go](main.go) for the complete
program.
