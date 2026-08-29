# Anvil — STDLIB Draft

## Status

This is a planning ledger. The final submission must rename/copy it to `STDLIB.md` and retain only substitutions actually present in the shipped artifact. A planned feature does not count.

## Zero-dependency claim

Anvil is implemented with Go 1.27 and imports only packages shipped in the Go standard library. The final `go.mod` must contain a module declaration and Go version but no `require` block. No `golang.org/x` module, copied package source, external service, or separately installed runtime executable is allowed.

The production HTTP server, parser, router, proxy, circuit breaker, metrics, SSE stream, benchmark client, and demo fixtures do not use `net/http` or `net/http/httputil`. Those standard-library packages may appear only in tests as an independent compatibility oracle.

## Planned substitutions

| Status | Normally installed | Standard-library implementation | Why it is meaningful |
|---|---|---|---|
| Implemented through Phase 4; recorded in `STDLIB.md` | `github.com/valyala/fasthttp` or a framework HTTP engine | `net`, `bufio`, `io`, `bytes` plus Anvil's HTTP/1.1 codec | Implements TCP lifecycle, message framing, keep-alive, chunked coding, and strict errors directly |
| Implemented through Phase 3; recorded in `STDLIB.md` | `github.com/gorilla/mux` or `github.com/go-chi/chi` | `strings` plus a method-aware route tree | Static, parameter, and wildcard matching without regex or router dependency |
| Mandatory / Package Killer | `github.com/sony/gobreaker` | `sync`, `sync/atomic`, `time` plus a closed/open/half-open state machine | Prevents traffic to failing upstreams and records explainable transitions |
| Mandatory | `github.com/prometheus/client_golang` | `sync/atomic`, fixed histogram buckets, `runtime/metrics`, custom JSON/SSE output | Bounded metrics and latency percentiles without a collector library |
| Mandatory | `github.com/r3labs/sse` or a WebSocket telemetry package | Custom chunked HTTP writer, `bufio`, bounded channels | Standards-compatible one-way live dashboard updates |
| Mandatory | `go.uber.org/zap`, `github.com/rs/zerolog`, or `github.com/sirupsen/logrus` | `log/slog` | Structured logs with levels and attributes |
| Implemented through Phase 4; recorded in `STDLIB.md` | `github.com/spf13/cobra` | `flag`, `os.Args` | Explicit subcommands, options, help, and exit behavior |
| Mandatory | `github.com/spf13/viper` | `encoding/json/v2`, `os`, `flag` | JSON configuration plus CLI overrides without YAML/TOML dependencies |
| Partially implemented through Phase 4; recorded in `STDLIB.md` | `github.com/google/uuid` | `crypto/rand` plus `encoding/hex` | Dependency-free 128-bit request IDs; experiment identifiers remain a later scope |
| Mandatory | `github.com/json-iterator/go` | `encoding/json/v2` after local API verification | Configuration, events, metrics, and receipts |
| Mandatory | `github.com/cenkalti/backoff` | `time` plus bounded retry/backoff logic | Explicit retry limits and timing |
| Mandatory | `github.com/hashicorp/go-retryablehttp` | `net`, custom serializer/parser, method-aware retry policy | Safe retries only before response commitment and only for replayable requests |
| Mandatory | `github.com/stretchr/testify` | `testing`, `testing/synctest` | Table tests, assertions through helpers, deterministic time/concurrency tests |
| Conditional | `golang.org/x/time/rate` or `github.com/juju/ratelimit` | `sync`, `time`, custom token bucket | Count only if per-client rate limiting ships |
| Conditional | compression middleware | `compress/gzip`, `compress/flate` | Count only if correct proxy transformation ships |
| Conditional | TLS wrapper | `crypto/tls`, `crypto/x509` | Count only if TLS termination/upstream support ships |
| Conditional | DNS library | `net`, `encoding/binary` | Count only if the authoritative `*.anvil.test` resolver ships |

## Flagship Package Killer: `sony/gobreaker`

The final claim must be precise: Anvil reimplements the circuit-breaker behavior it needs; it does not claim universal API compatibility.

Target semantics:

- Closed, open, and half-open states.
- Configurable interval/reset behavior.
- Configurable trip predicate based on counts.
- Cooldown timeout.
- Maximum half-open requests.
- Success classification.
- State-change callback into the decision ledger.
- Concurrency-safe counts and generation handling.

The final document must include:

- The exact supported behavior.
- Differences from `sony/gobreaker`.
- Tests demonstrating each transition.
- Why integrating the breaker with active health, passive signals, and the causal ledger is useful.

## Exact standard-library inventory

This list must be regenerated from shipped imports:

```text
bufio
bytes
context
crypto/rand or uuid (depending on verified Go 1.27 API)
crypto/sha256
encoding/binary
encoding/hex
encoding/json/v2
errors
flag
fmt
io
log/slog
math
net
os
os/signal
runtime/metrics
sort
strconv
strings
sync
sync/atomic
testing
testing/synctest
time
```

Unused entries must be removed. Stretch-only packages must not be listed unless shipped.

## Dependency proof plan

The final evidence should include commands and captured outputs such as:

```text
go env GOVERSION GOOS GOARCH CGO_ENABLED
go mod edit -json
go list -m all
go list -deps -f "{{if not .Standard}}{{.ImportPath}}{{end}}" .
```

Expected interpretation:

- `go mod edit -json` shows no requirements.
- `go list -m all` shows only the Anvil module.
- The non-standard dependency listing may show Anvil's own main package, but no external import path.

The exact command output belongs in `deps-proof.txt` and must be generated from the final checkout.

## Reproducible build plan

Candidate native build:

```text
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o dist/anvil.exe main.go
```

Candidate Linux AMD64 artifact:

```text
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o dist/anvil-linux-amd64 main.go
```

The final verifier builds twice using the same machine and Go 1.27 toolchain, computes SHA-256 with platform-standard tools, compares the bytes, and records both hashes. No reproducibility claim is made until this passes.

## Honesty checklist

- Do not call a standard-library import a custom implementation when it performs the core work.
- Do not count planned or dead code.
- Do not claim package parity without parity tests.
- Do not claim zero allocation; report measured allocations.
- Do not claim production readiness.
- Disclose `net/http` usage in tests even though it is allowed standard library.
- Explain why raw `net.Conn` was chosen even though `net/http` would be compliant.
