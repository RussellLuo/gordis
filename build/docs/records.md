# Build records

[中文](records.zh.md)

Successful delivery contains a binary and an adjacent `<output>.build/` directory:

```text
record.json          default envelope or application encoding
project/             generated files, final go.mod and go.sum when present
dependencies.json    go list -deps -json . object stream, not an array
logs/NN.log          captured command output, indexed by Commands
```

With `Adapter.Record == nil`, record.json is a two-space-indented JSON object with a final newline and `apiVersion: "gordis.build-record/v1alpha1"`. All Facts fields are flattened at its root. Empty collections use `[]`/`{}`. This format is separate from Qi's schemas.

| Field | Meaning |
| --- | --- |
| request | Original package selections, queries, requirements, source mappings and paths; preserves intent. |
| plan | Fixed package ownership, effective queries, require versions and replacements before generation. |
| modules | Go's actual final MVS graph; Path/Version/Sum/GoModSum and Replace use Go's field names. Require and actual versions can differ. |
| replacements | Every fixed mapping with an actual `used` flag, including unused mappings. |
| inputs | Absolute local module directories, used compiler/embed files and module files, per-file SHA-256 and aggregate SHA-256. Unused mappings still include their module files. |
| commands | Ordered stage, argv, relative log path, exit code, timeout and cancellation facts through the final input-discovery command. |
| environment | Selected toolchain/build facts, including GOVERSION, GOOS/GOARCH, CGO_ENABLED, GOWORK, GOFLAGS and GOTOOLCHAIN; not the whole process environment. |
| generatedSHA256 | Hashes of generated project files and final module files, keyed relative to project/. |
| binarySHA256 | SHA-256 of the delivered binary bytes. |
| localDevelopment | Local source mappings exist; this does not establish release readiness. |

Hashes are lowercase hexadecimal SHA-256. Each input aggregate hashes compact `json.Marshal(Files)` bytes with sorted map keys. Plan packages/requirements/replacements, modules and inputs have stable sorting; commands preserve execution order. Input comparisons are snapshots, not continuous filesystem isolation or a source archive. Different toolchains need not produce identical binaries.

GOFLAGS records the effective empty flag set as `""`. The execution environment uses whitespace to prevent fallback to persistent GOFLAGS, while preserving other Go proxy/cache configuration. Module-ownership probes that find no module remain in commands/logs, so a successful build may include probes with nonzero exit codes.

A custom `Record func(Facts) (map[string][]byte, error)` receives a detached post-validation snapshot and **replaces** the default encoder. It must return `record.json` containing a valid JSON object, and may add application files such as `metadata/selection.json`. The application owns its schema/version; the library does not merge default Facts back into that record. Mutating the snapshot cannot change the engine's plan, binary hash or logs. Like Generate, Record must be pure; cancellation is checked before and after the callback, not forcibly inside it.

File names must be canonical relative slash paths. Absolute paths, traversal, backslashes, case aliases and file/directory conflicts are rejected. `project/`, `logs/` and `dependencies.json` are library-owned, including case variants and descendants. Encoder errors, invalid files/JSON, cancellation or changed source abort before delivery. Either existing output path is refused. Normal failures clean this build's partial outputs; crash/power-loss atomicity across the two paths is not promised.

Tests in [record_test.go](../record_test.go) and [build_test.go](../build_test.go) exercise replacement encoding and additional files, isolated callback data, invalid output, encoder errors and cancellation using real compilation/validation. The public Go types and JSON field names are defined in [types.go](../types.go).
