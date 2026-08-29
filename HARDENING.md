# Anvil Phase 8 Hardening Report

## Outcome

Phase 8 added no product features. It audited and hardened the mandatory HTTP, proxy, resilience, observability, experiment, and lifecycle paths. Every confirmed defect received a regression test before its fix.

The final Phase 8 gate covers the full suite, MinGW-backed race detection, four native fuzz targets, allocation benchmarks, native curl compatibility, a real Chromium dashboard load, dependency proof, reproducible builds, and three consecutive offline demos.

## Confirmed defects and fixes

| Defect | Risk | Regression and correction |
|---|---|---|
| Scenario step arithmetic could overflow before schedule validation | A hostile JSON integer could produce a negative resolved time or panic the jitter calculation | Validate duration and step arithmetic without multiplication/addition overflow before scheduling |
| Scenario files had no outer byte limit | `os.ReadFile` could allocate from an arbitrarily large local input | Limit files and in-memory scenario input to 1 MiB before JSON decoding |
| Allocation-bearing configuration accepted platform-sized integers | Channels, maps, ledgers, HTTP bodies, and subscriber queues could be configured toward memory exhaustion | Add hard individual and aggregate caps before every corresponding allocation |
| 1xx and 204 upstream responses accepted forbidden Content-Length or Transfer-Encoding | Malformed no-content framing could contaminate persistent-connection interpretation | Reject forbidden framing as an upstream protocol error and close/discard the connection |
| Starting active health checks with a nil parent panicked | Invalid lifecycle input could terminate the process | Return a typed configuration error before creating a child context |

Preventive concurrency hardening also joins every context-triggered connection-close callback, joins the TCP shutdown watcher, publishes SSE events outside registry/order mutexes while preserving ledger sequence, and closes subscription lifecycle signals outside locks.

## P0/P1 stress coverage

- Actual HTTP Slowloris connection plus a normal client on the same listener.
- Slow upstream held open while a healthy backend completes independently.
- Connection refusal, incomplete body, malformed upstream status, slow SSE consumer, subscriber saturation, backend admission saturation, graceful drain, forced shutdown, and repeated start/stop.
- Status, `Proxy-Status`, ledger reason, metric counter, and address-privacy reconciliation.
- Strict allocation caps for downstream admission, backend pools, HTTP limits, ledgers, SSE queues, scenario files, CLI durations, and backend flag counts.
- Ordered concurrent observer delivery and deterministic joining of cancellation callbacks.

## Fuzzing

The committed corpus includes seeds for:

- Fixed and chunked request framing.
- Invalid line endings and HTTP/2 prefaces.
- Fixed, chunked, and malformed responses.
- Forbidden Content-Length and Transfer-Encoding on 1xx/204 responses.
- Valid, overflowing, extended, and quoted chunk sizes.
- Valid strict scenarios, unknown fields, and overflowing schedule integers.

Phase 8 ran each native target for ten seconds:

```powershell
go test -run '^$' -fuzz '^FuzzReadHTTPRequest$' -fuzztime 10s .
go test -run '^$' -fuzz '^FuzzReadHTTPResponse$' -fuzztime 10s .
go test -run '^$' -fuzz '^FuzzParseChunkSizeLine$' -fuzztime 10s .
go test -run '^$' -fuzz '^FuzzParseScenario$' -fuzztime 10s .
```

The recorded hardening run completed roughly 2.8 million executions without a crash or invariant failure.

## Measured allocation work

Command:

```powershell
go test -run '^$' -bench 'Benchmark(ReadHTTPRequest|BackendReserveRoundRobin)$' -benchmem -count=3 .
```

Representative repeated results on the project machine:

| Path | Before | After | Decision |
|---|---:|---:|---|
| Round-robin reserve/release | approximately 150 ns/op, 40 B/op, 2 allocs/op | approximately 122 ns/op, 16 B/op, 1 alloc/op | Removed the measured candidate-order slice from round robin; kept it only for least-in-flight sorting |
| End-to-end request decode benchmark | 6,480 B/op, 30 allocs/op | unchanged | No speculative rewrite; this benchmark intentionally includes a fresh buffered reader and parsed message ownership |

These values are machine-local measurements, not universal performance claims.

## Real compatibility evidence

With local `dev-http` and `dev-proxy` processes:

- Windows curl 8.21.0 completed HTTP/1.1 GET, fixed POST, and chunked POST with status 200 and exact bodies.
- The in-app Chromium browser loaded the offline dashboard, rendered the live state, completed card, backend topology, and decision timeline, and reported no console warnings or errors.

## Remaining limitations

- Anvil buffers request and response bodies; it does not stream arbitrary proxy bodies.
- HTTP/2, TLS termination, CONNECT, WebSocket/Upgrade, and `Expect: 100-continue` remain explicitly unsupported.
- SSE deliberately drops for slow subscribers and relies on the bounded ledger for replay.
- A connection handler that ignores both cancellation and a forcibly closed socket can outlive the configured force-close wait; `Serve` returns a lifecycle error rather than waiting forever.
- Benchmarks are development evidence, not production certification or cross-machine performance guarantees.
- The general JSON-configured `proxy` product command remains gated; `dev-proxy` is the manual operational surface.
