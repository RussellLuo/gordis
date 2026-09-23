# Service and lifecycle composition

This example combines two independent relationships: service slots connect
each collector to the intended store, while `Parent` makes `child-main`
lifecycle-owned by `collector-main`.

```text
store-main  ── store.primary ──> collector-main ── owns ──> child-main
store-audit ── store.audit   ──> collector-audit
```

## Run

From the repository root:

```sh
go run ./examples/composition
```

Expected output:

```text
collector-main / generation 1 -> store-main
collector-audit / generation 1 -> store-audit
close child-main
close collector-main using store-main
collector-main / generation 2 -> store-main
child-main / generation 2 belongs to collector-main / generation 2
close collector-audit using store-audit
close store-audit
close child-main
close collector-main using store-main
close store-main
```

## How it works

`Outputs` and `Inputs` map the same `storeKey` contract to `store.primary` and
`store.audit`, so each collector receives a different provider. `Parent` does
not create a service binding: it makes the child stop and restart with its
owner.

Stopping `collector-main` first cleans up `child-main`, then the collector.
Starting it again creates generation 2 for both, while the two store instances
remain running. Final shutdown cleans consumers and children before providers.

See [main.go](main.go) for the complete program.
