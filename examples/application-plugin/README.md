# Application plugin

This example delivers a versioned application plugin after the Host has
started. The `sampler` bundle contains a backend executable and browser UI. It
declares Alpha and Beta as separate processes that share one plugin page.

The Host provides only generic process, HTTP gateway, and UI-loading support;
it does not link the sampler implementation.

## Run

Requirements: Go 1.27+ and Node.js 22+.

In the first terminal, enter this example directory, then build and start the
Host:

```sh
cd examples/application-plugin
npm --prefix web ci
node build.mjs --host-only --output=/tmp/gordis-app-demo
/tmp/gordis-app-demo/host
```

Build sampler v1 in a second terminal:

```sh
cd examples/application-plugin
node build.mjs --packages-only --version=1.1.0 --output=/tmp/gordis-app-demo
```

Open <http://127.0.0.1:8090/demo/> and load v1.1.0. Alpha and Beta should have
different PIDs. The sampler page appears in the navigation, each instance has
its own panel, and the first sample returns `1007`.

To try an upgrade, re-enable any stopped instances, build v2, and select it in
the UI:

```sh
node build.mjs --packages-only --version=2.1.0 --output=/tmp/gordis-app-demo
```

The backend now returns `2007`, the UI version changes, and the Host PID stays
the same. Uninstalling removes the active instances, endpoints, and UI, but
keeps the package files available for loading again.

Package version directories are immutable. Use a new output directory, or move
the old one away, before rebuilding changed source with the same version.

## How it works

1. `build.mjs` writes the backend, manifest, and hashed UI assets to
   `packages/sampler/<version>`.
2. The running Host discovers the package and exposes it in its catalog.
3. The application Loader retains package/version, enabled state, and stable
   Instance handles. It translates package changes into
   `ChangeSet.Apply → Operation`; Gordis does not own package metadata.
4. Each backend is mounted from an Env that isolates its endpoint Service. Once
   ready, the Loader mounts a gateway child from `backend.Env()`, so the child
   shares that View and is reclaimed with its backend.
5. Each backend publishes a private HTTP endpoint. The generic gateway proxies
   it under `/api/extensions/<instance>/`.
6. When an instance is ready, the browser imports the validated UI module and
   registers its page and instance panel.
7. Disable, upgrade, and uninstall operations withdraw the gateway, drain
   requests, and stop the backend through `quiesce -> dispose -> shutdown`.

Start with [build.mjs](build.mjs), [host/app.go](host/app.go),
[host/gateway.go](host/gateway.go), [the sampler backend](plugins/sampler/main.go),
and [the UI loader](web/src/loader.ts).

## Verify

```sh
cd examples/application-plugin
make test
make check
make verify
```

The verifier covers real HTTP calls, child processes, upgrade, invalid-package
rejection, uninstall, and process reaping. It does not verify browser rendering.
