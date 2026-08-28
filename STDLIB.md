# Anvil — Standard-Library Substitution Log

## Scope

This file records only functionality present in the current build. `STDLIB_DRAFT.md` contains planned substitutions and is not evidence of shipped work.

Anvil is built with Go 1.27.0. Its `go.mod` has no `require` directive, production imports resolve only to the Go standard library, no package source is vendored, and the runtime does not invoke separately installed tools or services.

## Implemented substitutions — Phase 0

| Normally installed | Anvil implementation | Standard-library packages | Current boundary |
|---|---|---|---|
| CLI framework such as Cobra | Explicit subcommand dispatch, help, exit codes, and per-command flags | `flag`, `fmt`, `io`, `os` | Product commands are registered; only the development echo proof runs in Phase 0 |
| TCP server/framework | Raw listener, bounded admission, one goroutine per admitted connection, deadlines, and cancellation | `net`, `io`, `context`, `sync`, `time` | Echo proof only; HTTP parsing begins in Phase 1 |
| Assertion/test helper package | Table-driven validation checks, byte comparisons, real-socket concurrency test | `testing`, `bytes` | Phase 0 behavior only |

## Dependency verification

Run:

```sh
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
```

Expected output: no package paths after excluding Anvil's own main module.

`go list -m all` must list only the Anvil module itself.

## Honest status

The reverse proxy, HTTP codec, router, circuit breaker, metrics, dashboard, experiment runner, and benchmark engine are planned, not yet implemented, and therefore are not counted here.
