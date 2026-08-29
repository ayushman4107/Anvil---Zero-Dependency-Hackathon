# Anvil — Standard-Library Substitution Log

## Scope

This file records only functionality present in the current build. `STDLIB_DRAFT.md` contains planned substitutions and is not evidence of shipped work.

Anvil is built with Go 1.27.0. Its `go.mod` has no `require` directive, production imports resolve only to the Go standard library, no package source is vendored, and the runtime does not invoke separately installed tools or services.

Production HTTP code does not import `net/http` or `net/http/httputil`. Compatibility tests use `net/http.ReadRequest` and `net/http.ReadResponse` only as an independent wire-format oracle.

## Implemented substitutions — through Phase 3

| Normally installed | Anvil implementation | Standard-library packages | Current boundary |
|---|---|---|---|
| CLI framework such as Cobra | Explicit subcommand dispatch, help, exit codes, and per-command flags | `flag`, `fmt`, `io`, `os` | Product commands are registered; `dev-echo` and `dev-http` are clearly labelled runnable proofs |
| TCP server/framework | Raw listener, hard admission bound, goroutine ownership, I/O deadlines, bounded graceful drain, forced close, panic containment, and lifecycle counters | `net`, `io`, `context`, `sync`, `sync/atomic`, `time` | Reusable transport foundation now drives the raw HTTP server |
| HTTP engine such as `fasthttp` or a framework server | Hand-written incremental HTTP/1.1 request/response parser, strict framing state, fixed/chunked/close-delimited bodies, ordered fields, bounded trailers, typed failures, sequential persistence, error mapping, and pre-commit generated-response validation | `bufio`, `bytes`, `io`, `net`, `strconv`, `strings` | Network-facing server is working; upstream proxy behavior remains a later phase |
| HTTP serializer or `net/http/httputil` | Structured request/response reconstruction, accurate Content-Length, chunk writer, forbidden-body rules, header validation, and partial-write handling | `fmt`, `io`, `strconv`, `strings` | Used by codec tests; proxy sanitation is a later phase |
| Router such as Gorilla Mux or Chi | Immutable method-aware segment trie, static/parameter/wildcard precedence, raw escaped captures, deterministic 404/405 outcomes, and lock-free lookup | `strings`, `sort` | Route dispatch is working; no regex or router package is used |
| Assertion/test helper and protocol-fuzz package | Table-driven checks, fragmentation readers, deterministic mutation corpus, native fuzz targets, polling helpers, byte comparisons, real sockets, and a `net/http` compatibility oracle | `testing`, `bytes`, `math/rand` | Covers malformed framing, bounded limits, serializer safety, lifecycle, route concurrency, fragmented traffic, persistence, and standard-client reuse |

## Dependency verification

Run:

```sh
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
```

Expected output: no package paths after excluding Anvil's own main module.

`go list -m all` must list only the Anvil module itself.

## Honest status

The reverse proxy transaction, circuit breaker, product metrics, dashboard, experiment runner, and benchmark engine are planned, not yet implemented, and therefore are not counted here. Phase 1's atomic lifecycle counters are internal transport evidence, not a claim that the product metrics system is complete. Phase 3's development HTTP server is not a claim that upstream proxying exists.
