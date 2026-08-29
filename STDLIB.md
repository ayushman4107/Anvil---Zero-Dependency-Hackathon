# Anvil — Standard-Library Substitution Log

## Scope

This file records only functionality present in the current build. `STDLIB_DRAFT.md` contains planned substitutions and is not evidence of shipped work.

Anvil is built with Go 1.27.0. Its `go.mod` has no `require` directive, production imports resolve only to the Go standard library, no package source is vendored, and the runtime does not invoke separately installed tools or services.

Production HTTP code does not import `net/http` or `net/http/httputil`. Compatibility tests use `net/http.ReadRequest` and `net/http.ReadResponse` only as an independent wire-format oracle.

## Implemented substitutions — through Phase 8

| Normally installed | Anvil implementation | Standard-library packages | Current boundary |
|---|---|---|---|
| CLI framework such as Cobra | Explicit subcommand dispatch, help, exit codes, repeatable upstream flags, per-command validation, and assertion-controlled experiment status | `flag`, `fmt`, `io`, `os`, `os/signal` | `demo`, `experiment`, and `bench` ship; configured `proxy` remains explicitly gated |
| TCP server/framework | Raw listener, hard admission bound, goroutine ownership, I/O deadlines, bounded graceful drain, forced close, panic containment, and lifecycle counters | `net`, `io`, `context`, `sync`, `sync/atomic`, `time` | Reusable transport foundation now drives the raw HTTP server |
| HTTP engine such as `fasthttp` or a framework server | Hand-written incremental HTTP/1.1 request/response parser, strict framing state, fixed/chunked/close-delimited bodies, ordered fields, bounded trailers, typed failures, sequential persistence, error mapping, and pre-commit generated-response validation | `bufio`, `bytes`, `io`, `net`, `strconv`, `strings` | The same codec now serves downstream clients and parses upstream responses |
| HTTP serializer or `net/http/httputil` | Structured request/response reconstruction, accurate Content-Length, chunk writer, forbidden-body rules, header validation, hop-by-hop sanitation, partial-write handling, and fully buffered replay | `fmt`, `io`, `strconv`, `strings` | Drives forwarding, active probes, and method-aware safe retry without a high-level HTTP client |
| Router such as Gorilla Mux or Chi | Immutable method-aware segment trie, static/parameter/wildcard precedence, raw escaped captures, deterministic 404/405 outcomes, and lock-free lookup | `strings`, `sort` | Route dispatch is working; no regex or router package is used |
| Reverse-proxy library or `net/http/httputil.ReverseProxy` | Bounded transaction attempts, round-robin/least-in-flight eligibility, per-backend admission, deadline-bound dial/write/read, authority rewrite, forwarding metadata, `Via`, typed gateway failures, commitment state, and RFC 9209 `Proxy-Status` | `net`, `bufio`, `context`, `sync`, `sync/atomic`, `time` | Working fixed/chunked resilient proxy; generated diagnostics expose backend aliases rather than addresses |
| Circuit breaker such as `sony/gobreaker` | Backend-local closed/open/half-open state machine, rolling passive failure interval, cooldown, bounded probes, success evidence, transition callbacks, and coherent snapshots | `sync`, `sync/atomic`, `time` | Flagship Package Killer behavior; callbacks execute outside locks and virtual-time tests avoid sleeps |
| Health-check framework | One bounded worker per backend using Anvil's raw TCP HTTP codec, separate failure/recovery thresholds, explicit start/cancel/join lifecycle, and circuit half-open probe integration | `context`, `net`, `bufio`, `sync`, `time` | Active health is opt-in in `dev-proxy`; passive outcomes remain a distinct circuit input |
| Retry/backoff package | GET/HEAD-only replay policy, downstream commitment guard, distinct-backend exclusion, attempt cap, total-time cap, and explicit application-status opt-in | `context`, `time`, `strings` | Unsafe methods are never automatically retried; no retry occurs after response commitment |
| Upstream connection-pool package | Per-backend bounded idle stack, idle expiry, deadline reset, fully parsed keep-alive reuse, and discard on every unsafe lifecycle outcome | `net`, `sync`, `time` | No maintenance goroutine; pool close drains idle sockets and active transactions retain ownership |
| Metrics stack such as Prometheus client libraries | Atomic request/status/error/byte/transition counters, active/peak gauges, fixed latency buckets, estimated percentiles, backend/TCP snapshots, and selected runtime samples | `sync/atomic`, `runtime/metrics`, `math`, `time` | In-memory JSON snapshot; no exporter, scrape dependency, or claim of exact quantiles |
| Structured event/ledger package | Typed metadata-only events, monotonic sequence assignment, fixed-capacity ring, bounded replay, explicit expired/future cursor gaps, and ordered delivery turns with fan-out outside locks | `sync`, `time`, `encoding/json` | No bodies, sensitive headers, or backend addresses exist in the event schema; observer sends occur outside mutexes |
| SSE/WebSocket telemetry package | Separate raw-TCP admin handler, validated chunked headers, native chunk writer, event IDs/types, heartbeat comments, Last-Event-ID replay, bounded subscribers/queues, lifecycle signals, and drop counters | `net`, `bufio`, `context`, `sync`, `sync/atomic`, `time` | Non-blocking fan-out snapshots subscribers before send; tests use `net/http` only as a wire oracle |
| Dashboard framework and asset pipeline | Inline responsive HTML/CSS/vanilla JavaScript, same-origin JSON polling and EventSource timeline, topology cards, rates, status, estimated latency, and runtime/drop visibility | Go raw string constant, `encoding/json` | Offline and single-file-compatible; no CDN, npm, generated bundle, or external asset |
| Load generator such as hey, wrk, or vegeta | Fixed worker set, optional pacing, bounded duration/count, owned persistent connections, wire-byte accounting, error/status classes, cancellation, and fixed latency buckets | `net`, `bufio`, `context`, `sync`, `sync/atomic`, `time` | Uses Anvil's serializer/parser; no external process or high-level HTTP client |
| Chaos/fixture framework | In-process loopback backends with atomic immutable healthy, delayed, failure, truncated, unavailable, and recovered profiles | `net`, `sync/atomic`, `time` | Uses Anvil's TCP lifecycle and HTTP codec; unavailable dial behavior is injected at Anvil's dial seam |
| Scenario/configuration framework | Strict unknown-field-rejecting JSON, hard resource limits, seeded relative jitter, stable normalized encoding, and SHA-256 identity | `encoding/json`, `crypto/sha256`, `math/rand`, `sort` | Phase 7 experiment schema only; general proxy route/pool JSON remains gated |
| Assertion/reporting framework | Ledger-derived success/failure streak, failover and recovery measurements, benchmark reconciliation, stable text, and JSON receipts | `encoding/json`, `fmt`, `sort` | Failed assertions produce a non-zero process exit; receipts are controlled-lab evidence, not certification |
| UUID/request-ID package | 128 random bits with UUID version/variant bits encoded as a bounded lowercase token | `crypto/rand`, `encoding/hex` | Generated IDs replace untrusted inbound Anvil IDs and remain stable across each transaction |
| Assertion/test helper and protocol-fuzz package | Table-driven checks, fragmentation readers, deterministic mutation corpus, virtual clocks, four native fuzz targets, allocation benchmarks, byte comparisons, real sockets, raw failure fixtures, and a `net/http` compatibility oracle | `testing`, `bytes`, `math/rand`, `sync/atomic` | Also covers Slowloris/isolation, allocation caps, joined cancellation, forbidden no-content framing, observer ordering, privacy, reconciliation, SSE saturation, and browser/curl compatibility |

## Dependency verification

Run:

```sh
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
```

Expected output: no package paths after excluding Anvil's own main module.

`go list -m all` must list only the Anvil module itself.

## Honest status

The experiment runner, fault fixtures, benchmark engine, canonical scenario hash, assertions, resilience receipts, and Phase 8 hardening gates are implemented. General JSON route/pool configuration is still planned, so `proxy` remains gated and `dev-proxy` remains the manual proxy surface. Scenarios, receipts, compatibility checks, and benchmarks describe bounded local evidence; they do not claim production readiness or certification.
