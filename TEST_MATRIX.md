# Anvil — Test Matrix

## Purpose

Tests are submission evidence. The suite combines pure parser tests, arbitrary-fragmentation cases, fuzzing, deterministic state-machine tests, real sockets, compatibility clients, race detection, chaos experiments, dependency proof, and reproducible-build verification.

Priorities: **P0** blocks the feature, **P1** blocks submission, and **P2** is additional or stretch confidence.

## Standard commands

```text
go test ./...
go test -race ./...
go test -fuzz FuzzRequestParser -fuzztime 60s
go vet ./...
gofmt -w main.go main_test.go
```

No third-party test framework is permitted or needed.

## HTTP parser and framing

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| HTTP-001 | P0 | Minimal valid GET | Correct method, target, version, Host, empty body |
| HTTP-002 | P0 | Input one byte at a time | Same result as contiguous input |
| HTTP-003 | P0 | Random fragmentation corpus | Result independent of read segmentation |
| HTTP-004 | P0 | Two requests already buffered | First parsed; second bytes preserved |
| HTTP-005 | P0 | Fixed POST body | Exactly declared octets consumed |
| HTTP-006 | P0 | Premature EOF | Incomplete request; connection unsafe |
| HTTP-007 | P0 | Bytes after fixed body | Extra bytes retained for next message |
| HTTP-008 | P0 | Valid chunked body | Correct decoded content |
| HTTP-009 | P0 | Fragmented chunk delimiters | Correct incremental decode |
| HTTP-010 | P0 | Zero chunk and trailers | Bounded trailers validated |
| HTTP-011 | P0 | Invalid/overflowing chunk size | Protocol error and close |
| HTTP-012 | P0 | Transfer-Encoding plus Content-Length | Rejected as ambiguous |
| HTTP-013 | P0 | Conflicting Content-Length values | Rejected |
| HTTP-014 | P1 | Repeated identical Content-Length | Matches documented policy |
| HTTP-015 | P0 | Unsupported transfer coding | Rejected safely |
| HTTP-016 | P0 | Missing Host | Rejected for HTTP/1.1 |
| HTTP-017 | P0 | Whitespace before header colon | Rejected |
| HTTP-018 | P0 | Obsolete folded header | Rejected |
| HTTP-019 | P0 | Bare LF / CRLF injection | Rejected |
| HTTP-020 | P0 | Header byte/count limit | Bounded failure before excess allocation |
| HTTP-021 | P0 | Body-size limit | `413` and unsafe connection closed |
| HTTP-022 | P1 | HEAD response | No body octets |
| HTTP-023 | P1 | 1xx/204/304 response | No body octets |
| HTTP-024 | P1 | Close-delimited upstream response | Read until close within bounds |
| HTTP-025 | P1 | HTTP/2 preface, Upgrade, CONNECT | Explicit safe rejection |
| HTTP-026 | P1 | Expect: 100-continue | Implemented or consistent `417` |
| HTTP-027 | P0 | Parser fuzz target | No panic, hang, unbounded allocation, or invalid success |

## Router

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| ROUTE-001 | P0 | Exact static route | Correct handler |
| ROUTE-002 | P0 | Method separation | GET and POST resolve independently |
| ROUTE-003 | P0 | Static versus parameter | Static wins |
| ROUTE-004 | P0 | Parameter capture | Correct name and value |
| ROUTE-005 | P0 | Terminal wildcard | Correct suffix capture |
| ROUTE-006 | P0 | Ambiguous duplicate registration | Configuration error |
| ROUTE-007 | P1 | Query string | Excluded from path matching |
| ROUTE-008 | P1 | Percent-decoding edges | Exactly one documented decode policy |
| ROUTE-009 | P1 | Concurrent lookup | Race-free immutable reads |

## Writer and connection lifecycle

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| RESP-001 | P0 | Fixed response | Correct CRLF and Content-Length |
| RESP-002 | P0 | Chunked response | Valid chunks and terminal zero chunk |
| RESP-003 | P0 | Header value containing CR/LF | Serialization refused |
| RESP-004 | P0 | Sequential keep-alive | Multiple round trips on one connection |
| RESP-005 | P0 | Connection: close | Response then close |
| RESP-006 | P1 | Idle timeout | Connection reclaimed predictably |
| RESP-007 | P1 | Disconnect during write | No leak or process crash |

## Proxy correctness

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| PROXY-001 | P0 | Client → Anvil → fixture | Method, target, body, end-to-end fields preserved |
| PROXY-002 | P0 | Connection-nominated fields | Removed before forwarding |
| PROXY-003 | P0 | Known hop-by-hop fields | Removed or regenerated correctly |
| PROXY-004 | P0 | Via | Appended correctly |
| PROXY-005 | P0 | Authority rewrite | Configured backend Host observed |
| PROXY-006 | P0 | Untrusted forwarding fields | Not treated as authoritative |
| PROXY-007 | P1 | IPv4/IPv6 Forwarded formatting | Valid and sanitized |
| PROXY-008 | P0 | Request ID | Valid and stable per transaction |
| PROXY-009 | P0 | Upstream refusal | `502`, Proxy-Status, ledger event |
| PROXY-010 | P0 | Upstream timeout | `504`, Proxy-Status, passive failure |
| PROXY-011 | P0 | Invalid upstream response | `502` before commit; upstream discarded |
| PROXY-012 | P0 | Safe GET retry before commit | Bounded retry to eligible backend |
| PROXY-013 | P0 | POST failure | No automatic retry by default |
| PROXY-014 | P0 | Failure after downstream commit | No retry; clean termination |
| PROXY-015 | P1 | Unknown end-to-end field | Preserved |
| PROXY-016 | P1 | Body cap | `413` before upstream attempt |
| PROXY-017 | P0 | Persistent upstream, sequential requests | One connection safely reused |
| PROXY-018 | P0 | Upstream `Connection: close` | Socket discarded; next request redials |
| PROXY-019 | P1 | Configured application status | GET retries only when policy opts in |
| PROXY-020 | P0 | Proxy-Status privacy | RFC error token plus alias; no private address |

## Balancing, health, and circuit breaker

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| RES-001 | P0 | Round robin, three healthy nodes | Stable fair rotation |
| RES-002 | P0 | Least in-flight | Busy node deprioritized |
| RES-003 | P0 | No eligible backend | `503` without panic |
| RES-004 | P0 | Active probe failures | Ineligible at configured threshold |
| RES-005 | P0 | Passive timeout/refusal | Failure counter and transition input |
| RES-006 | P0 | Closed → open | Exact trip rule and one event |
| RES-007 | P0 | Open circuit | No normal selection |
| RES-008 | P0 | Cooldown → half-open | Limited probes only |
| RES-009 | P0 | Half-open success | Recovery threshold closes circuit |
| RES-010 | P0 | Half-open failure | Reopens and restarts cooldown |
| RES-011 | P0 | Concurrent failures | Race-free counters and coherent transition |
| RES-012 | P1 | Virtual-time state tests | No real sleeps |
| RES-013 | P1 | All transaction exit paths | In-flight count returns to baseline |
| RES-014 | P0 | Active recovery threshold | Ineligible backend restored only after required successes |
| RES-015 | P0 | Active half-open probe | Success closes; failure reopens |
| RES-016 | P1 | Transition callback re-entry | Snapshot succeeds without deadlock |

## Concurrency and lifecycle

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| CONC-001 | P0 | Slowloris plus normal clients | Normal clients complete within bound |
| CONC-002 | P0 | Admission saturation | Bounded rejection; no goroutine explosion |
| CONC-003 | P0 | Many simultaneous clients | Correct responses; no race report |
| CONC-004 | P0 | Slow upstream | Other upstream traffic stays responsive |
| CONC-005 | P0 | Slow SSE subscriber | Proxy unaffected; drops counted |
| CONC-006 | P0 | Graceful shutdown | Acceptance stops; bounded drain; workers join |
| CONC-007 | P1 | Repeated start/stop | No goroutine/connection accumulation |
| CONC-008 | P1 | Race detector | No reported races |

## Telemetry, dashboard, and receipt

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| OBS-001 | P0 | Ledger capacity wrap | Latest bounded events and monotonic IDs |
| OBS-002 | P0 | Sensitive headers/body | Never present in events |
| OBS-003 | P0 | Metrics under concurrency | Counters reconcile with known workload |
| OBS-004 | P0 | Histogram fixture | Expected bucket percentile estimates |
| OBS-005 | P0 | SSE wire format | Content type, chunks, IDs, heartbeat valid |
| OBS-006 | P1 | Last-Event-ID retained | Correct replay |
| OBS-007 | P1 | Last-Event-ID expired | Explicit gap/reset event |
| OBS-008 | P0 | Default admin bind | Loopback only |
| OBS-009 | P0 | Receipt derivation | Values match source ledger events |
| OBS-010 | P0 | Config/scenario hash | Stable for canonical identical input |

## Experiment and benchmark

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| EXP-001 | P0 | Demo startup | Proxy, admin, fixtures, and load run locally |
| EXP-002 | P0 | Delay fault | Tail latency rises; passive signal recorded |
| EXP-003 | P0 | Backend unavailable | Detection, ejection, continued traffic |
| EXP-004 | P0 | Recovery | Half-open probe and return to rotation |
| EXP-005 | P0 | Assertion pass/fail | Correct process exit status |
| EXP-006 | P1 | Same seed | Same schedule and fault sequence |
| BENCH-001 | P0 | Bounded workers | Configured concurrency never exceeded |
| BENCH-002 | P0 | Known response counts | Status and error totals reconcile |
| BENCH-003 | P0 | Cancellation | Workers and connections terminate |
| BENCH-004 | P1 | ANSI disabled | Stable plain-text output |
| BENCH-005 | P1 | JSON output | Valid schema and receipt integration |

## Compatibility, compliance, and builds

| ID | Pri | Test | Expected result |
|---|---:|---|---|
| COMPAT-001 | P0 | Go `net/http` client oracle | Real client round trips through Anvil |
| COMPAT-002 | P0 | `curl` GET/POST/chunked | Expected results |
| COMPAT-003 | P0 | Browser dashboard | No console or network errors |
| COMPAT-004 | P1 | Fragmented raw client | Correct response |
| BUILD-001 | P0 | One-command native build | Runnable artifact |
| BUILD-002 | P0 | `go.mod` audit | No `require` block |
| BUILD-003 | P0 | Module/dependency audit | No external module |
| BUILD-004 | P1 | Build twice | Byte-identical SHA-256 hashes |
| BUILD-005 | P1 | Clean checkout | Instructions are sufficient |
| BUILD-006 | P1 | Single-file audit if claimed | One implementation source file |
| BUILD-007 | P0 | Documentation audit | README, STDLIB, limits, license, proof present |

## Acceptance gates

1. **Codec gate:** all HTTP P0 parser, framing, writer, fragmentation, and fuzz-seed tests pass.
2. **Proxy gate:** real client → Anvil → fixture works; sanitation and error mappings pass.
3. **Resilience gate:** selection, active/passive health, circuit transitions, and race tests pass.
4. **Product gate:** offline demo, dashboard, experiment, benchmark, ledger, and receipt work together.
5. **Submission gate:** clean build, dependency proof, reproducible hashes, documentation, and video are complete.
