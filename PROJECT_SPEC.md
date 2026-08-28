# Anvil — Project Specification

## Document status

- Project: Anvil
- Track: C — Web & Network
- Language/toolchain: Go 1.27, standard library only
- Team: two active members
- Event window: 28 August 2026 18:00 UTC to 31 August 2026 18:00 UTC
- Product class: explainable reverse proxy and resilience-testing laboratory
- Tagline: **Break the backend. Keep the edge. Explain every decision.**

This document defines the product to be built. `PROTOCOL_INVARIANTS.md` is authoritative for wire behavior, `ARCHITECTURE.md` for component boundaries, and `TEST_MATRIX.md` for acceptance evidence.

## 1. Product thesis

Anvil is a single-binary local reliability appliance. It accepts real HTTP/1.1 traffic, routes it across backend services, detects degradation and failure, removes unsafe backends, and explains every decision through a live dashboard, standards-based proxy diagnostics, and a final resilience receipt.

The core differentiator is not merely that failover happens. Anvil records why it happened, how quickly it happened, what traffic was affected, and whether a repeatable experiment met its stated objectives.

## 2. Problem

Testing service failure behavior normally requires several separately installed tools: a reverse proxy, circuit-breaker library, health checker, load generator, metrics collector, dashboard, and fault-injection system. That setup is disproportionate for local development, teaching, small teams, and fast reliability experiments.

Anvil collapses that workflow into one offline-capable executable with an empty dependency manifest.

## 3. Intended users

- Backend developers validating local service failover.
- SRE and platform engineers reproducing resilience behavior in a small test environment.
- Students learning HTTP framing, proxies, concurrency, load balancing, and circuit breakers.
- Hackathon judges who need a self-contained, inspectable demonstration of standard-library craft.

## 4. Primary user journeys

### 4.1 Proxy real services

The user starts Anvil with a JSON configuration containing one or more routes and upstream pools. Real browser or command-line HTTP clients send traffic to Anvil. Anvil selects healthy upstreams and forwards requests.

### 4.2 Run a self-contained demo

`anvil demo` starts the proxy, administration dashboard, multiple in-process backend fixtures, a bounded load generator, and a predefined failure experiment. It must not require internet access or separately installed services.

### 4.3 Run a resilience experiment

`anvil experiment` executes a deterministic scenario schedule against the in-process fixtures. The scenario can introduce delay, error responses, connection refusal, incomplete responses, and recovery. Anvil evaluates assertions and emits a JSON and human-readable resilience receipt.

### 4.4 Benchmark the data path

`anvil bench` generates bounded concurrent HTTP load using Anvil's own HTTP client code and reports throughput, error classes, connection reuse, and latency percentiles.

## 5. Mandatory scope

### 5.1 Raw HTTP/1.1 engine

- TCP listener using `net`, not `net/http`.
- Goroutine-per-connection concurrency with a global admission limit.
- Incremental parsing over a persistent `bufio.Reader`.
- Request line, response line, header, fixed-length body, and chunked body processing.
- Correct response serialization and chunked response writing.
- Sequential HTTP/1.1 keep-alive; pipelining is explicitly unsupported.
- Configurable request-line, header, body, connection, and timeout limits.
- Strict rejection of ambiguous or malformed framing.
- Real interoperability with browsers, `curl`, and the Go standard-library HTTP client.

### 5.2 Method-aware route tree

- Static, named-parameter, and terminal-wildcard segments.
- Static-over-parameter-over-wildcard precedence.
- Route registration validation.
- Internal administration routes and route-to-upstream-pool dispatch.
- No regular-expression dependency.

### 5.3 Reverse proxy

- Structured request reconstruction rather than raw first-chunk mutation.
- Required hop-by-hop header removal.
- Safe authority/Host rewriting.
- `Via` support.
- Configurable `Forwarded` and compatibility `X-Forwarded-*` handling.
- Request IDs.
- `Proxy-Status` diagnostics using backend aliases rather than private addresses.
- Bounded whole-body buffering in the first stable version.
- Automatic retries only where replay is safe and before downstream response bytes are committed.

### 5.4 Load balancing and resilience

- Round-robin and least-in-flight selection.
- Active HTTP health checks.
- Passive signals from real request failures and latency.
- Per-backend closed, open, and half-open circuit states.
- Cooldown, limited half-open probes, recovery threshold, and state-change events.
- Global and per-upstream admission limits.
- Honest `502`, `503`, and `504` classifications.

### 5.5 Causal observability

- Immutable structured events with monotonic sequence numbers.
- Fixed-capacity in-memory decision ledger.
- Fixed-bucket latency histograms.
- Request, status, error, byte, connection, backend, and circuit metrics.
- Non-blocking event fan-out; slow observers cannot block proxy traffic.
- Administration listener bound to loopback by default.
- Inline HTML/CSS/JavaScript dashboard.
- Server-Sent Events over the custom chunked response writer.

### 5.6 Experiment and evidence system

- In-process demo backends built on Anvil's own server engine.
- Repeatable scenario schedule with seed and configuration hash.
- Faults: latency, configured HTTP failures, incomplete response, unavailable backend, and recovery.
- Integrated concurrent load generator.
- Assertions for success rate, maximum failure streak, failover time, and recovery.
- Resilience receipt in JSON and readable text.

### 5.7 CLI and configuration

Mandatory commands:

```text
anvil proxy
anvil demo
anvil experiment
anvil bench
```

Configuration uses JSON because Go has no standard-library YAML or TOML parser. CLI behavior must have useful help, non-zero failure exit codes, stable stdout for machine output, and diagnostics on stderr.

## 6. Stretch scope

These features may be attempted only after all mandatory acceptance gates pass:

- DNS authoritative test resolver for `*.anvil.test`, on an unprivileged port.
- TLS listener or HTTPS upstream through `crypto/tls`.
- General static-file serving.
- Standalone network diagnostics.
- Latency-aware power-of-two-choices balancing.
- Upstream keep-alive connection pool.
- Streaming proxy bodies.
- Per-client token-bucket rate limiting.
- Gzip transformation with correct HTTP metadata handling.

## 7. Explicit non-goals

- HTTP/2 or HTTP/3.
- CONNECT tunnelling.
- WebSocket proxying.
- Transparent interception or TLS man-in-the-middle behavior.
- ACME certificate automation.
- Distributed state or persistent metrics.
- General-purpose production gateway parity with Caddy, NGINX, HAProxy, or Traefik.
- Dependence on a database, public DNS resolver, cloud API, CDN, or internet connection.
- Unqualified claims of zero allocations, production readiness, or a specific concurrency capacity.

## 8. Product success criteria

Anvil is successful only if all of the following are true:

1. A clean checkout builds using one documented Go command.
2. `go.mod` contains no `require` block.
3. The binary runs an offline self-contained demonstration.
4. Real HTTP clients can communicate with the raw server and proxy.
5. A slow client cannot block unrelated clients.
6. Ambiguous framing is rejected safely.
7. Traffic continues when one backend fails, subject to documented detection limits.
8. The decision ledger explains selection, failure, circuit transition, and recovery.
9. The receipt's measurements are derivable from ledger events.
10. The team can explain every major state machine and limitation.

## 9. Scoring strategy

### Functionality and usefulness — 35%

- One command starts a useful local reliability lab.
- The main demo works offline and shows a real service problem.
- Real clients and configurable real upstreams are supported.

### Zero-Dependency Craft — 30%

- Raw HTTP/1.1 over `net.Conn` and `bufio`.
- Custom router, proxy sanitation, circuit breaker, metrics, SSE, experiment runner, and benchmark engine.
- Detailed `STDLIB.md` and dependency proof.

### Code quality and idiom — 25%

- Small explicit state machines.
- Typed error classes.
- Concrete components with narrow interfaces only at test seams.
- Table-driven, fuzz, concurrency, and real-socket tests.

### Innovation — 10%

- Causal decision ledger.
- RFC-based `Proxy-Status` explanations.
- Deterministic failure experiments.
- Resilience receipts.

## 10. Bonus policy

- **STDLIB Log:** target 10 or more real substitutions.
- **Package Killer:** target the relevant behavior of `github.com/sony/gobreaker`; document supported semantics and differences honestly.
- **Reproducible Build:** build twice on the same machine and Go toolchain and publish matching SHA-256 hashes.
- **Single File:** attempt only if the final implementation remains readable. Tests, docs, fixtures, and build scripts may remain separate under the organizer clarification.

## 11. Required repository artifacts

```text
main.go                    # if Single File remains viable
main_test.go               # tests may remain separate
go.mod                     # module declaration, no require block
README.md
STDLIB.md
deps-proof.txt
.zero-dep.toml
LICENSE
build.ps1 / build.sh       # optional convenience; one direct command must remain documented
verify-repro.ps1
testdata/                  # protocol corpora and experiment fixtures
```

## 12. Honest limitations to disclose

- HTTP/1.1 only.
- No request pipelining.
- Initial proxy buffers bodies up to a configured limit.
- No request retry after any downstream response bytes are written.
- No automatic retry for unsafe methods.
- Metrics are in-memory and reset on restart.
- The default administration plane is local-only and has no remote authentication.
- Anvil is a development and resilience-testing tool, not a production edge gateway.
