# Anvil

**Every failover leaves a receipt.** Anvil is an explainable reverse proxy and resilience-testing lab built for Track C of the Zero Dependency Hackathon. It makes routing, retry, circuit-breaker, and recovery decisions observable and reproducible from one offline-capable binary.

> Submission status: the mandatory failure/recovery story is complete and frozen. Experiment shutdown joins every lab service before fixture state is captured in the receipt.

## The problem

Local resilience tests usually require a proxy, load generator, fault fixtures, metrics stack, dashboard, and several configuration layers. When a request fails over, those disconnected tools rarely produce one causal answer to *why* it happened or whether recovery met an explicit objective.

Anvil puts that lab behind one zero-dependency executable. The same decision ledger drives the live dashboard and a deterministic resilience receipt containing the seed, normalized configuration hash, measured failover/recovery timings, assertions, and benchmark/ledger reconciliation.

## Sixty-second quick start

```powershell
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o anvil.exe .
.\anvil.exe demo
```

The demo starts two loopback fixtures, the real Anvil proxy, and Anvil's own bounded benchmark client. It injects a fault, observes failover, restores the backend, observes recovery, and exits non-zero if any declared assertion fails. Nothing is downloaded and no external service is contacted.

For machine-readable evidence:

```powershell
.\anvil.exe demo --json-out receipt.json
```

## What is inside

```text
client / benchmark
       |
bounded TCP admission and connection lifecycle
       |
custom HTTP/1.1 parser -> immutable route tree -> resilient proxy attempt loop
                                                   |            |
                                      backend selector     bounded pools
                                                   |            |
                                          loopback/upstream fixtures
       |
causal ledger + atomic metrics -> loopback-only JSON / SSE / dashboard
       |
deterministic resilience receipt and assertions
```

The data plane owns protocol and routing decisions. Observability receives bounded metadata after decisions and never controls backend selection. The experiment runner composes the same production components; it is not a simulated state-machine demo.

## Concurrency model

| Owner | Bound and lifecycle |
|---|---|
| TCP server | One goroutine per admitted connection, a hard global admission cap, joined handlers, bounded graceful drain, then forced socket closure |
| Backend | Fixed in-flight semaphore and bounded idle-connection pool; every reservation has idempotent exactly-once release |
| Health checker | At most one worker per backend; cancellation closes active sockets and `Stop` joins every worker |
| Benchmark | Fixed worker set; each worker owns its connection and all workers join before results are returned |
| SSE | Fixed subscriber count and queue sizes; non-blocking fan-out occurs without a registry mutex held, with explicit drop metrics |
| Metrics/ledger | Atomics for counters and a fixed-capacity sequenced ring; callbacks and network I/O occur outside state locks |

## Protocol and security boundary

- HTTP/1.1 only, with an incremental parser over raw TCP; production code does not import `net/http` or `net/http/httputil`.
- Ambiguous framing, conflicting lengths, unsupported transfer codings, obs-fold, bare LF, forbidden trailers, and forbidden 1xx/204 framing are rejected before reuse.
- Client-controlled lines, headers, bodies, chunks, trailers, connections, requests, backends, workers, queues, ledgers, and time values have validated hard limits before allocation or conversion.
- Inbound forwarding metadata and Anvil request IDs are replaced. Telemetry has no header, cookie, authorization, body, or backend-address field.
- Automatic retry is limited to fully buffered `GET`/`HEAD`, a distinct eligible backend, pre-commit state, an attempt cap, and a total-time cap. Unsafe methods are never retried automatically.
- The read-only administration listener requires an explicit loopback IP and is never registered on the public proxy listener.
- TLS termination, HTTP/2, streaming proxy bodies, and production certification are deliberately outside the submitted scope.

## Requirements

- Go 1.27.0
- No third-party modules, services, or runtime executables
- A supported C compiler only when running Go's optional race detector (`-race`); ordinary builds remain `CGO_ENABLED=0`

## Build

From the repository root on Windows:

```powershell
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o anvil.exe .
```

On Linux or macOS:

```sh
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o anvil .
```

Run the applicable checks:

```sh
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
```

On Windows, run `.\verify-zero-dep.ps1` to enforce the source and module boundary. It fails if `go.mod` gains dependency directives; if `go.sum`, `go.work`, or `vendor/` appears; if the module graph gains another module; if production imports a non-standard package; or if production imports `net/http`.

Run `.\verify-repro.ps1` from PowerShell to execute that zero-dependency gate, build twice in an isolated temporary directory, and fail unless both SHA-256 hashes match.

The repository also ships `.githooks/pre-commit`, which runs the same gate before a commit. Enable it once per clone with `git config core.hooksPath .githooks`; this checkout already has it enabled. The hook uses `go` from `PATH`, or the executable selected by `ANVIL_GO` when the toolchain is intentionally portable.

## Command surface

```text
anvil proxy
anvil demo
anvil experiment
anvil bench
anvil dev-proxy
anvil dev-http
anvil dev-echo
```

`demo`, `experiment`, and `bench` are operational. The configured `proxy` product command remains explicitly gated until the JSON route/pool configuration phase; `dev-proxy` remains the runnable manual proxy surface.

Run the complete built-in offline story:

```sh
anvil demo --json-out receipt.json
```

Run the checked-in strict scenario or benchmark any local HTTP/1.1 endpoint:

```sh
anvil experiment --scenario examples/failure-recovery.json --json-out receipt.json
anvil bench --target 127.0.0.1:8080 --requests 1000 --concurrency 8
```

The experiment process exits non-zero when initialization, execution, or any declared assertion fails. `--json` sends a machine-readable result to stdout; otherwise Anvil prints a stable plain-text receipt.

The TCP lifecycle proof can be started with:

```sh
anvil dev-echo --listen 127.0.0.1:8080
```

It accepts raw TCP clients concurrently and exercises the same reusable lifecycle foundation the HTTP engine will use.

The raw HTTP server proof can be started with:

```sh
anvil dev-http --listen 127.0.0.1:8080
```

It exposes `GET /health`, `GET /hello/:name`, and `POST /echo` through Anvil's own parser, router, and serializer. It is deliberately labelled as a development proof rather than the unfinished proxy product.

The observable proxy proof accepts one or more repeatable upstreams:

```sh
anvil dev-proxy --listen 127.0.0.1:8080 \
  --admin-listen 127.0.0.1:9090 \
  --upstream api-a=127.0.0.1:9001 \
  --upstream api-b=127.0.0.1:9002 \
  --selector least-in-flight \
  --health-checks
```

All valid methods and origin-form paths are forwarded. Round robin is the default; `--selector least-in-flight` deprioritizes busy eligible nodes. `GET` and `HEAD` may fail over before downstream commitment within the attempt/time bounds, while unsafe methods are never retried automatically. Active checks are opt-in for arbitrary development backends and use `GET /health` by default.

Open `http://127.0.0.1:9090/` for the dashboard. The administration listener is separate from proxy traffic and accepts only an explicit loopback IP. Its current read-only routes are:

| Route | Purpose |
|---|---|
| `GET /` | Inline HTML/CSS/JavaScript dashboard |
| `GET /api/metrics` | JSON counters, histogram estimates, backend and runtime snapshots |
| `GET /api/events` | Chunked SSE stream with replay and heartbeat comments |
| `GET /healthz` | Administration listener health |

Ledger, subscriber, and SSE queue limits are configurable with `--ledger-capacity`, `--max-subscribers`, and `--subscriber-queue`.

## TCP foundation

The shipped server now provides:

- One goroutine per admitted connection and no goroutine allocation for rejected connections.
- A hard global admission limit with immediate bounded rejection.
- Independent read, write, and idle deadlines refreshed at I/O boundaries.
- Graceful shutdown that stops acceptance, permits a bounded drain, then cancels and closes stragglers.
- Explicit ownership and joining of connection goroutines.
- Atomic accepted, admitted, rejected, active, peak, completed, handler-error, and forced-close counters.
- Containment of connection-handler panics so one faulty client path cannot crash the process.
- Real-socket tests for saturation, slow-client isolation, deadline reclamation, graceful drain, forced close, repeated lifecycle use, and concurrent echo traffic.

## HTTP/1.1 codec

The codec operates on `bufio.Reader` and `io.Writer` boundaries without `net/http` or `net/http/httputil`. It currently provides:

- Ordered request, response, header, trailer, and body-framing types.
- Strict HTTP/1.1 request-line and status-line parsing.
- Origin-form targets and `OPTIONS *`; CONNECT, absolute-form, HTTP/2 prefaces, upgrades, and `Expect` are rejected explicitly.
- Incremental CRLF line handling whose result is independent of TCP fragmentation.
- Configurable start-line, header-byte, header-count, body, chunk-line, chunk-count, trailer-byte, and trailer-count limits.
- Required single valid Host authority, including bracketed IPv6 and numeric ports.
- Exact fixed-length framing with unread next-message bytes preserved.
- Chunked decoding with bounded validated extensions and trailers.
- Rejection of Transfer-Encoding plus Content-Length, conflicting lengths, unsupported codings, obs-fold, bare LF, control bytes, and forbidden trailer fields.
- Fixed and chunked request/response serialization with CRLF-injection prevention and short-write handling.
- Correct body suppression for HEAD, informational, 204, and 304 responses.
- Rejection of forbidden Content-Length or Transfer-Encoding on 1xx and 204 upstream responses.
- Fixed, chunked, and close-delimited upstream response parsing.
- Table-driven protocol cases, arbitrary-fragment readers, deterministic mutation coverage, and native Go fuzz targets.

## HTTP server and route tree

The codec is connected to the bounded TCP lifecycle. The network-facing slice provides:

- One `bufio.Reader` and writer per admitted connection, with sequential HTTP/1.1 keep-alive and a configurable request-count bound.
- Correct default persistence and explicit closure for `Connection: close`, request-limit exhaustion, protocol errors, and internal handler failures.
- Safe protocol mappings: malformed requests return `400`, oversized bodies `413`, unsupported transfer codings `501`, and unsupported expectations `417`; each parse failure closes the connection.
- Buffered generated-response validation before socket commit, including a bounded `500` fallback for handler errors, panics, nil responses, or unsafe generated headers.
- A method-first immutable segment trie with static, `:parameter`, and terminal `*wildcard` routes, plus an explicit any-method bucket.
- Static-over-parameter-over-wildcard precedence, deterministic `Allow` values for `405`, and distinction between `404` and `405`.
- Query exclusion and an explicit no-decode policy: the router matches the validated raw escaped path exactly once and returns raw escaped captures, so `%2F` is never double-decoded into a segment separator.
- Real-socket tests for standard-library client reuse, one-byte fragmentation, multiple buffered requests, closure, error mapping, route outcomes, panic containment, and request-count limits.
- Concurrent frozen-router lookup tests suitable for the race detector on supported toolchains.

Requests are processed one at a time on each connection. Anvil does not execute pipelined requests concurrently; a fully buffered next request is retained and processed only after the previous response completes.

## Bounded reverse-proxy transaction

The first complete client-to-upstream vertical slice now provides:

- Immutable backend identities and stable atomic round-robin selection across a configured pool.
- A hard per-backend in-flight admission bound with idempotent exactly-once reservation release.
- Context-aware TCP dialing plus independent upstream dial, write, and response deadlines.
- Structured upstream request reconstruction through Anvil's own writer—never mutation of an assumed first TCP read.
- Parsing of `Connection` before removal of every nominated field and all known hop-by-hop fields.
- Authority/`Host` rewriting, `Via` append in both directions, and cryptographically random per-transaction `X-Anvil-Request-ID` values.
- Default distrust and replacement of inbound `Forwarded` and `X-Forwarded-*` values using the immediate validated peer address.
- Preservation of duplicate and unknown end-to-end headers, bounded bodies, chunked coding, and permitted trailers.
- Strict upstream response parsing, bounded informational responses, and reconstruction of close-delimited responses with a safe `Content-Length`.
- Honest single-attempt mappings: refusal, incomplete, or malformed upstreams produce `502`; admission exhaustion produces `503`; dial/read/write timeouts produce `504`.
- Conservative downstream commitment state that becomes immutable before encoded response bytes enter the downstream writer.
- Real-socket tests using two Anvil-engine fixtures, a standard-library client oracle, malformed raw fixtures, one-attempt proof, and concurrent admission stress.

## Resilience core

The proxy transaction boundary is governed by a backend-local resilience engine:

- Immutable backend identity/configuration is separated from mutex-protected health/circuit state, atomic in-flight counts, bounded admission, and an independently locked idle-connection pool.
- Stable round-robin and least-in-flight selectors consider both active health and circuit permission. A health-ineligible or open backend is never selected.
- Active checks use Anvil's own TCP client, request writer, and response parser. Failure and recovery thresholds prevent one noisy probe from flapping eligibility, and checker cancellation joins every worker.
- Passive refusal, timeout, incomplete/protocol response, configured application status, and optional slow-latency signals feed a rolling failure window.
- Circuits transition `closed -> open -> half-open -> closed/open`. Cooldown only permits a bounded probe; elapsed time alone never restores service. Transition callbacks run after the backend lock is released.
- Only fully buffered `GET` and `HEAD` requests can be retried, only before downstream commitment, only to a distinct eligible backend, and only within configured attempt and total-time bounds. `POST` is never retried automatically.
- Application statuses are not retried unless explicitly configured. If no alternate backend is available, the buffered upstream response is returned rather than replaced with a fabricated gateway error.
- Per-backend keep-alive pools have fixed idle capacities and expiry. Only completely parsed persistent responses are reusable; close-delimited, `Connection: close`, failed, timed-out, canceled, malformed, and surplus connections are discarded.
- Every upstream response carries an Anvil `Proxy-Status` member. Generated failures use RFC 9209 error tokens, while `next-hop` contains only the configured public backend alias—not its private address.
- Snapshots expose coherent health/circuit/counter state for the ledger and dashboard without making observability own data-plane state.

Deterministic virtual-time state-machine tests cover opening, exclusion, bounded half-open admission, recovery, cooldown restart, callback re-entry, active health thresholds, safe retry, unsafe-method refusal, reuse/discard, and concurrent state access. The complete suite is race-detector compatible with the documented MinGW-w64 toolchain on Windows.

## Causal observability

The observability plane makes resilience decisions inspectable without becoming part of their control path:

- A fixed-capacity ring assigns monotonic sequence IDs under one short memory-only critical section. Retained replay reports oldest/latest sequence and explicit gaps.
- Events contain only request IDs, route/backend aliases, typed reasons, state transitions, status, attempt, and numeric timing. The schema has no body, header, cookie, authorization, or backend-address fields.
- Each request records start, selection, failed attempts, retry decisions, and completion. Circuit and active-health callbacks add exact previous/new state events after backend locks are released.
- Request, attempt, retry, success, gateway-error, byte, status-class, failure-kind, circuit, health, active, and peak counters use atomics.
- Latency uses fixed upper-bound buckets; p50/p95/p99 values are explicitly labelled estimates.
- The JSON snapshot also includes coherent backend state, TCP lifecycle counters, and selected `runtime/metrics` values.
- SSE uses Anvil's own parser and chunked writer with event IDs, typed events, heartbeat comments, `Last-Event-ID` replay, future/expired cursor gaps, fixed subscriber count, bounded queues, and observer-drop counters.
- Event fan-out uses non-blocking sends. A full observer queue loses events and increments a counter; it never waits on proxy traffic.
- The dashboard is one inline, offline-capable HTML/CSS/vanilla-JavaScript constant with topology, request rate, success/status signals, estimated p50/p95/p99, in-flight state, runtime information, drop visibility, and the causal timeline.
- The admin listener is loopback-only and read-only. No dashboard, event, metric, or future mutation route is registered on the public proxy listener.

Real-socket tests validate browser-consumable JSON/HTML, native chunked SSE framing through a standard-library client oracle, heartbeat, retained replay, expired replay gaps, subscriber saturation, shutdown, privacy, concurrent metrics, and slow-observer isolation.

## Deterministic experiments and receipts

The product story is now self-contained and evidence-driven:

- Loopback fixtures use Anvil's TCP lifecycle, request parser, and response writer. Their immutable atomic profiles support healthy, delayed, configured failure, deliberately truncated, unavailable, and recovered behavior.
- Scenarios are decoded with unknown-field rejection and strict trailing-data checks. Fixture counts, step counts, workers, requests, durations, timeouts, and ledger capacity all have hard limits.
- `seed` resolves optional per-step jitter reproducibly. The normalized validated scenario is encoded in a stable struct order and identified by a SHA-256 configuration hash.
- Scenario transitions are appended to the causal ledger before the atomic fixture profile changes, preserving the decision timeline used by the receipt.
- The benchmark engine uses Anvil's request serializer and response parser. A fixed worker set owns its connections and reports pacing, status/error classes, transferred wire bytes, new/reused connections, peak in-flight work, throughput, and fixed-bucket latency estimates.
- Receipts derive request success, failure streak, failover, and recovery from sequenced ledger events. Benchmark/ledger reconciliation is itself an assertion rather than an assumed property.
- The built-in `demo` and checked-in `examples/failure-recovery.json` execute the same canonical scenario. Three consecutive offline recovery runs are part of the automated suite.

## Protocol and concurrency hardening

The hardening audit changed behavior only where it found a correctness or resource-safety defect:

- Scenario files are capped at 1 MiB, and schedule validation uses overflow-safe integer comparisons before duration conversion or jitter resolution.
- Downstream connections, per-connection requests, backend counts/admission/idle pools, HTTP limits, ledger entries, SSE subscribers/queues, scenario load, and CLI time values are bounded before allocation or conversion.
- Upstream 1xx and 204 responses carrying forbidden framing are rejected and discarded instead of entering the persistent pool.
- Context-triggered socket closers and the TCP shutdown watcher have explicit join paths.
- SSE registry locks are released before fan-out. Concurrent publishers retain exact ledger/live sequence through an ordering barrier that performs no observer send while locked.
- Native fuzzing now covers request, response, chunk-size, and strict-scenario parsers. Dedicated tests exercise an actual HTTP Slowloris, slow-upstream isolation, cancellation joining, mapping/privacy reconciliation, and allocation-scale rejection.
- Measured round-robin selection dropped from two allocations/40 bytes to one allocation/16 bytes per reserve/release operation on the project machine; no unmeasured parser rewrite was attempted.

## Measured benchmarks

Submission measurements were captured on Windows/amd64 with Go 1.27.0 and an AMD Ryzen AI 7 350. They are microbenchmarks, not end-to-end capacity claims:

```text
go test -run '^$' -bench 'Benchmark(ReadHTTPRequest|BackendReserveRoundRobin)$' -benchmem -benchtime=2s -count=5 .
```

| Benchmark | Five-run timing | Memory | Allocations |
|---|---:|---:|---:|
| Parse a representative HTTP/1.1 request | 2.343–2.671 us/op (median 2.420 us/op) | 6,480 B/op | 30 allocs/op |
| Round-robin reserve/release | 127.6–134.4 ns/op (median 131.9 ns/op) | 16 B/op | 1 alloc/op |

Results are machine-local evidence. They do not imply Internet-scale throughput, exact production latency, or a zero-allocation parser.

## Bonus claims

| Bonus | Submission decision | Evidence |
|---|---|---|
| STDLIB Log | Claimed | `STDLIB.md` records only shipped substitutions and the exact direct-import inventory |
| Package Killer | Claimed for the required circuit-breaker behavior, not API compatibility | `STDLIB.md` compares supported state, threshold, cooldown, probe, callback, health, and ledger semantics with `sony/gobreaker`; deterministic transition and race tests cover Anvil's behavior |
| Reproducible Build | Claimed | `verify-repro.ps1` performs the pinned build twice; `deps-proof.txt` records the matching SHA-256 hashes |
| Single File | **Not claimed** | 6,212 production lines across 27 Go files are intentionally kept componentized; merging them during freeze would make the result harder to audit and explain |

## Submission verification

The release gate exposed a receipt snapshot racing the fixture handler's final accounting. Experiments now stop health workers, close idle upstreams, cancel and join every lab server, and only then capture ledger and fixture state. The assertion-failure CLI test uses deterministic unavailable fixtures instead of timing assumptions.

The checked-in dependency gate, reproducibility verifier, standard test suite, race tests, fuzz targets, compatibility tests, deterministic experiments, and receipt assertions provide the submission evidence. Exact dependency and build-hash output is captured in `deps-proof.txt`.

## Competitive differentiator

Anvil's competitive edge is causal resilience evidence: a bounded decision ledger explains why an upstream was selected or skipped, when health and circuit state changed, what the client observed, and how the system recovered. Deterministic experiments turn that ledger into a machine-readable and human-readable resilience receipt.

## Zero-dependency boundary

- `go.mod` contains no `require` directive.
- Production code imports only the Go standard library.
- Anvil does not vendor or copy third-party package source.
- The runtime does not shell out to tools or depend on network services.
- The raw server and proxy core do not use `net/http`; tests may use it only as an independent compatibility oracle.

See `STDLIB.md` for the final submission inventory and dependency-boundary details.

## Current limitations

The submission does not claim production certification or the Single File bonus. The general `proxy` command remains gated until JSON route/pool configuration exists. Experiment scenarios intentionally support GET load only and bounded in-memory receipts. Metrics are process-local and histogram percentiles are bucket estimates. SSE is intentionally lossy for slow subscribers, with replay limited to the retained ledger. Proxy bodies remain buffered and non-streaming; active health checks remain opt-in for arbitrary development backends without `/health`. A handler that ignores cancellation and a forcibly closed socket can outlive the force-close wait, in which case the server returns a lifecycle error instead of waiting forever.

## License

MIT, an OSI-approved license. See `LICENSE` and the [OSI MIT license entry](https://opensource.org/license/mit).
