# Anvil

Anvil is an explainable reverse proxy and resilience-testing lab built for Track C of the Zero Dependency Hackathon. Its core promise is to make every routing, failover, circuit-breaker, and recovery decision observable and reproducible from one offline-capable binary.

> Phase 5 status: the raw-TCP HTTP engine now drives a bounded reverse proxy with round-robin or least-in-flight selection, active/passive health, closed/open/half-open circuits, safe pre-commit retries, upstream connection reuse, and RFC 9209 `Proxy-Status`. Product telemetry and experiments remain later gates.

## Requirements

- Go 1.27.0
- No third-party modules, services, or runtime executables

## Build

From the repository root on Windows:

```powershell
go build -trimpath -buildvcs=false -o anvil.exe .
```

On Linux or macOS:

```sh
go build -trimpath -buildvcs=false -o anvil .
```

Run the applicable checks:

```sh
go fmt ./...
go test ./...
go vet ./...
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
```

The final command excludes Anvil's own main module and must print no package paths.

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

These product commands are registered and currently fail with an explicit status instead of pretending unfinished functionality exists.

The TCP lifecycle proof can be started with:

```sh
anvil dev-echo --listen 127.0.0.1:8080
```

It accepts raw TCP clients concurrently and exercises the same reusable lifecycle foundation the HTTP engine will use.

The Phase 3 HTTP proof can be started with:

```sh
anvil dev-http --listen 127.0.0.1:8080
```

It exposes `GET /health`, `GET /hello/:name`, and `POST /echo` through Anvil's own parser, router, and serializer. It is deliberately labelled as a development proof rather than the unfinished proxy product.

The Phase 5 proxy proof accepts one or more repeatable upstreams:

```sh
anvil dev-proxy --listen 127.0.0.1:8080 \
  --upstream api-a=127.0.0.1:9001 \
  --upstream api-b=127.0.0.1:9002 \
  --selector least-in-flight \
  --health-checks
```

All valid methods and origin-form paths are forwarded. Round robin is the default; `--selector least-in-flight` deprioritizes busy eligible nodes. `GET` and `HEAD` may fail over before downstream commitment within the attempt/time bounds, while unsafe methods are never retried automatically. Active checks are opt-in for arbitrary development backends and use `GET /health` by default.

## Phase 1 TCP foundation

The shipped server now provides:

- One goroutine per admitted connection and no goroutine allocation for rejected connections.
- A hard global admission limit with immediate bounded rejection.
- Independent read, write, and idle deadlines refreshed at I/O boundaries.
- Graceful shutdown that stops acceptance, permits a bounded drain, then cancels and closes stragglers.
- Explicit ownership and joining of connection goroutines.
- Atomic accepted, admitted, rejected, active, peak, completed, handler-error, and forced-close counters.
- Containment of connection-handler panics so one faulty client path cannot crash the process.
- Real-socket tests for saturation, slow-client isolation, deadline reclamation, graceful drain, forced close, repeated lifecycle use, and concurrent echo traffic.

## Phase 2 HTTP/1.1 codec

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
- Fixed, chunked, and close-delimited upstream response parsing.
- Table-driven protocol cases, arbitrary-fragment readers, deterministic mutation coverage, and native Go fuzz targets.

## Phase 3 HTTP server and route tree

The codec is now connected to the bounded Phase 1 TCP lifecycle. The network-facing slice provides:

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

## Phase 4 bounded reverse-proxy transaction

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

## Phase 5 resilience core

The Phase 4 transaction boundary is now governed by a backend-local resilience engine:

- Immutable backend identity/configuration is separated from mutex-protected health/circuit state, atomic in-flight counts, bounded admission, and an independently locked idle-connection pool.
- Stable round-robin and least-in-flight selectors consider both active health and circuit permission. A health-ineligible or open backend is never selected.
- Active checks use Anvil's own TCP client, request writer, and response parser. Failure and recovery thresholds prevent one noisy probe from flapping eligibility, and checker cancellation joins every worker.
- Passive refusal, timeout, incomplete/protocol response, configured application status, and optional slow-latency signals feed a rolling failure window.
- Circuits transition `closed -> open -> half-open -> closed/open`. Cooldown only permits a bounded probe; elapsed time alone never restores service. Transition callbacks run after the backend lock is released.
- Only fully buffered `GET` and `HEAD` requests can be retried, only before downstream commitment, only to a distinct eligible backend, and only within configured attempt and total-time bounds. `POST` is never retried automatically.
- Application statuses are not retried unless explicitly configured. If no alternate backend is available, the buffered upstream response is returned rather than replaced with a fabricated gateway error.
- Per-backend keep-alive pools have fixed idle capacities and expiry. Only completely parsed persistent responses are reusable; close-delimited, `Connection: close`, failed, timed-out, canceled, malformed, and surplus connections are discarded.
- Every upstream response carries an Anvil `Proxy-Status` member. Generated failures use RFC 9209 error tokens, while `next-hop` contains only the configured public backend alias—not its private address.
- Snapshots expose coherent health/circuit/counter state for the next phase's ledger and dashboard without making observability own data-plane state.

Deterministic virtual-time state-machine tests cover opening, exclusion, bounded half-open admission, recovery, cooldown restart, callback re-entry, active health thresholds, safe retry, unsafe-method refusal, reuse/discard, and concurrent state access. The complete suite is race-detector compatible with the documented MinGW-w64 toolchain on Windows.

## Planned differentiator

Anvil's competitive edge is causal resilience evidence: a bounded decision ledger explains why an upstream was selected or skipped, when health and circuit state changed, what the client observed, and how the system recovered. Deterministic experiments turn that ledger into a machine-readable and human-readable resilience receipt.

The mandatory design, protocol boundaries, test strategy, demo, and execution gates are documented in:

- `PROJECT_SPEC.md`
- `ARCHITECTURE.md`
- `PROTOCOL_INVARIANTS.md`
- `TEST_MATRIX.md`
- `DEMO_SCRIPT.md`
- `KICKOFF_EXECUTION_PLAN.md`

## Zero-dependency boundary

- `go.mod` contains no `require` directive.
- Production code imports only the Go standard library.
- Anvil does not vendor or copy third-party package source.
- The runtime does not shell out to tools or depend on network services.
- The raw server and proxy core do not use `net/http`; tests may use it only as an independent compatibility oracle.

See `STDLIB.md` for substitutions actually implemented through Phase 5. Planned substitutions remain in `STDLIB_DRAFT.md` and do not count as shipped work.

## Current limitations

Phase 5 is a working resilient development proxy, but the product `proxy` command remains gated until JSON route/pool configuration and the administration plane exist. There is no causal event ledger, metrics/histograms, SSE dashboard, deterministic experiment runner, resilience receipt, or benchmark engine yet. Retries remain intentionally buffered and non-streaming; active health checks are opt-in on `dev-proxy` so arbitrary backends without `/health` do not become unexpectedly ineligible.

## License

MIT. See `LICENSE`.
