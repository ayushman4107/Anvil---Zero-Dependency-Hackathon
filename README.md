# Anvil

Anvil is an explainable reverse proxy and resilience-testing lab built for Track C of the Zero Dependency Hackathon. Its core promise is to make every routing, failover, circuit-breaker, and recovery decision observable and reproducible from one offline-capable binary.

> Phase 2 status: the reusable raw-TCP lifecycle and strict transport-agnostic HTTP/1.1 codec are implemented and tested. The reverse proxy and resilience features are intentionally not claimed yet.

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
```

These product commands are registered and currently fail with an explicit status instead of pretending unfinished functionality exists.

The TCP lifecycle proof can be started with:

```sh
anvil dev-echo --listen 127.0.0.1:8080
```

It accepts raw TCP clients concurrently and exercises the same reusable lifecycle foundation the HTTP engine will use.

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

The codec is not connected to the public TCP listener yet. That vertical integration, sequential keep-alive loop, routing, and proxy transaction belong to the next phase.

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
- The raw server and proxy core will not use `net/http`; tests may use it only as an independent compatibility oracle.

See `STDLIB.md` for substitutions actually implemented so far. Planned substitutions remain in `STDLIB_DRAFT.md` and do not count as shipped work.

## Current limitations

Phase 2 is not a reverse proxy. It does not yet route requests, connect to upstreams, balance backends, run health checks, open circuits, publish product telemetry, or execute experiments. These are acceptance-gated phases, and the README will be updated only when each capability is working and tested.

## License

MIT. See `LICENSE`.
