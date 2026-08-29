# Anvil — Standard-Library Substitution Log

## Scope

This file records only functionality present in the current build. `STDLIB_DRAFT.md` contains planned substitutions and is not evidence of shipped work.

Anvil is built with Go 1.27.0. Its `go.mod` has no `require` directive, production imports resolve only to the Go standard library, no package source is vendored, and the runtime does not invoke separately installed tools or services.

## Implemented substitutions — through Phase 1

| Normally installed | Anvil implementation | Standard-library packages | Current boundary |
|---|---|---|---|
| CLI framework such as Cobra | Explicit subcommand dispatch, help, exit codes, and per-command flags | `flag`, `fmt`, `io`, `os` | Product commands are registered; the development echo proof is the only runnable data path |
| TCP server/framework | Raw listener, hard admission bound, goroutine ownership, I/O deadlines, bounded graceful drain, forced close, panic containment, and lifecycle counters | `net`, `io`, `context`, `sync`, `sync/atomic`, `time` | Reusable transport foundation; HTTP parsing starts in Phase 2 |
| Assertion/test helper package | Table-driven checks, polling helpers, byte comparisons, and real-socket lifecycle/concurrency tests | `testing`, `bytes` | Covers saturation, isolation, timeouts, shutdown, repeated start/stop, panic containment, and concurrent traffic |

## Dependency verification

Run:

```sh
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
```

Expected output: no package paths after excluding Anvil's own main module.

`go list -m all` must list only the Anvil module itself.

## Honest status

The HTTP codec, reverse proxy, router, circuit breaker, product metrics, dashboard, experiment runner, and benchmark engine are planned, not yet implemented, and therefore are not counted here. Phase 1's atomic lifecycle counters are internal transport evidence, not a claim that the product metrics system is complete.
