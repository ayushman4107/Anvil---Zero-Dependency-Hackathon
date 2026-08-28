# Anvil — Kickoff Execution Plan

## Operating principles

1. Correctness before breadth.
2. Every phase ends with compiling tests and a runnable vertical slice.
3. No third-party imports, copied package source, runtime shell-outs, or external services.
4. No feature proceeds past its acceptance gate while its foundation is unstable.
5. Documentation and `STDLIB.md` are updated while decisions are fresh.
6. A feature without a test and demo path is not finished.
7. The two members work on separate ownership areas but integrate frequently.

## Team ownership

### Member A — protocol and data plane

- TCP listener and lifecycle.
- HTTP request/response codec.
- Framing and protocol limits.
- Router.
- Proxy sanitation and transaction path.
- Compatibility and fuzz tests.

### Member B — resilience and product plane

- Backend model and selectors.
- Active/passive health.
- Circuit breaker.
- Metrics and ledger.
- SSE/dashboard.
- Demo fixtures, experiment runner, benchmark, receipt.
- Submission documentation and video assembly.

### Shared responsibilities

- Review state ownership and error paths.
- Run race tests.
- Keep `main` buildable.
- Explain each other's components.
- Make scope-cut decisions together.

If working toward one `main.go`, ownership is by clearly marked sections and function/type regions. Avoid simultaneous edits to the same region.

## Integration cadence

- Integrate at least every 3–4 working hours.
- Each integration must compile and pass the currently applicable tests.
- Do not allow long-lived unintegrated rewrites.
- Before handing work across, record: files/sections changed, invariants affected, tests added, commands run, known limitations, and the next smallest step.

## 72-hour relative schedule

The clock below is relative to official kickoff. If execution begins after a checkpoint, perform a status audit and start at the earliest incomplete gate rather than pretending elapsed time is available.

### Hours 0–4 — Compliance and skeleton

Deliverables:

- Confirm Go 1.27 executable.
- Create the project directory and repository after kickoff.
- Create `go.mod` without requirements.
- Add license and minimal README/STDLIB skeletons.
- Define CLI command dispatch and configuration types without feature sprawl.
- Establish `go test`, `go vet`, and `gofmt` commands.
- Implement a concurrent raw TCP echo proof.

Gate:

- Clean build.
- No external imports.
- Multiple echo clients do not block each other.

### Hours 4–14 — HTTP codec

Member A:

- Request/response/header types.
- Bounded start-line and header reader.
- Fixed-length body framing.
- Response serializer.
- Connection loop and sequential keep-alive.

Member B:

- Table-test harness and fragmentation helpers.
- Configuration validation and typed error taxonomy.
- Documentation of protocol limits and decisions.

Gate A1:

- Real standard-library client reaches a handler through raw TCP server.
- Contiguous and one-byte-fragmented requests parse identically.
- Fixed bodies, malformed headers, limits, and keep-alive tests pass.

### Hours 14–22 — Chunked coding and router

Member A:

- Chunked decoder, trailers, and chunked writer.
- Response-body rules for HEAD/1xx/204/304.
- Method-aware route tree.

Member B:

- Chunk fragmentation/error tests.
- Router precedence and ambiguity tests.
- Initial fuzz corpus and fuzz target.

Gate A2:

- HTTP P0 tests pass.
- Chunked request and SSE-capable response writer work over real sockets.
- Fuzz seeds do not panic or hang.

### Hours 22–34 — Reverse proxy vertical slice

Member A:

- Upstream dial and request serialization.
- Upstream response parser reuse.
- Hop-by-hop sanitation, Host rewrite, Via, request ID.
- Bounded body forwarding.

Member B:

- In-process backend fixtures.
- Basic round robin and backend counters.
- Proxy integration tests and error mapping.

Gate B:

- Real client → Anvil → two fixtures works.
- Request/response bodies and end-to-end fields are preserved.
- Refusal, timeout, and malformed upstream responses produce consistent gateway errors.
- No retry occurs after downstream commitment.

### Hours 34–44 — Resilience core

Member A:

- Least-in-flight integration.
- Retry transaction bookkeeping and safe method policy.
- Proxy-Status generation.

Member B:

- Active health loop.
- Passive outcome classification.
- Closed/open/half-open circuit breaker.
- Deterministic state-machine tests using an injectable/virtual clock.

Gate C:

- All P0 selection/circuit tests pass.
- A failed fixture is excluded and later restored through half-open probing.
- Concurrent transitions pass the race detector.

### Hours 44–54 — Ledger, metrics, and dashboard

Member A:

- Emit structured events at protocol/proxy/resilience boundaries.
- Audit sensitive-data exclusion.

Member B:

- Ring ledger, counters, histogram, SSE hub.
- Loopback administration listener.
- Inline dashboard with topology, throughput, latency, and timeline.

Gate D1:

- Slow dashboard subscriber cannot affect proxy throughput.
- Ledger wraps correctly.
- Metrics reconcile against a known workload.
- Browser dashboard receives valid SSE from the custom writer.

### Hours 54–62 — Experiment, benchmark, and receipt

Member A:

- Benchmark client connection/request path.
- Error classification and cancellation.

Member B:

- Scenario parser/validator.
- Fixture fault controls.
- Assertion evaluator and resilience receipt.

Gate D2:

- One command runs the complete offline failure/recovery story.
- Receipt fields derive from ledger events.
- Benchmark workers and duration are bounded.
- Scenario pass/fail controls the exit status.

### Hours 62–67 — Hardening

- Full P0/P1 test pass.
- Parser fuzzing.
- Race detector.
- Slowloris, slow upstream, disconnect, and graceful-shutdown tests.
- Real browser and `curl` compatibility.
- Profile allocations and remove only demonstrated hot spots.
- Fix errors; do not add major features.

Gate D3:

- No known P0 defect.
- No data race.
- Three consecutive successful offline demos.

### Hours 67–70 — Submission evidence and bonuses

- Finalize README and STDLIB.
- Generate `deps-proof.txt`.
- Add `.zero-dep.toml` and OSI-approved license.
- Verify clean one-command build.
- Build twice and compare hashes.
- Decide whether Single File is still defensible.
- If consolidation is necessary, perform it only with a full test rerun.
- Record final limitations and benchmark environment.

### Hours 70–72 — Video and freeze

- Record the five-minute demo and a backup take.
- Verify public repository state and final commit.
- Re-run dependency/build smoke checks.
- Confirm links and video permissions.
- Submit before the deadline with buffer; do not use the last minutes for features.

## Critical path

```text
TCP lifecycle
  → strict HTTP codec
  → real client compatibility
  → reverse proxy transaction
  → backend selection
  → health/circuit breaker
  → ledger/metrics
  → experiment/benchmark/receipt
  → evidence and video
```

Nothing may bypass this path. DNS, TLS, static serving, diagnostics, compression, adaptive balancing, and rate limiting remain outside it.

## Scope-cut order

Cut in this order when a gate is late:

1. DNS.
2. TLS.
3. General static serving.
4. Standalone diagnostics.
5. Gzip transformation.
6. Per-client rate limiting.
7. Adaptive P2C selection.
8. Upstream connection pooling.
9. Streaming proxy bodies.
10. Single File bonus.
11. Additional dashboard polish.
12. Extra fault types.

Never cut:

- Correct TCP fragmentation handling.
- Framing security.
- Concurrency limits/timeouts.
- Basic proxying.
- Round robin and least-in-flight.
- Circuit breaker and failover demonstration.
- Causal ledger and receipt.
- Tests, README, STDLIB, dependency proof, or demo video.

## Risk register

| Risk | Early signal | Response |
|---|---|---|
| Parser consumes wrong number of bytes | Keep-alive/fragmentation failures | Stop feature work; fix codec and add minimal failing corpus |
| Proxy retries unsafe request | Duplicate fixture side effect | Default retries to GET/HEAD; track downstream commit and buffered body |
| Circuit state races | Race detector or duplicate transitions | Backend-local transition lock and generation tests |
| Dashboard blocks traffic | Latency changes with slow SSE client | Bounded non-blocking subscriber queues |
| Single-file merge destabilizes code | Conflicts/duplicate helpers | Drop bonus or merge before final hardening, never during freeze |
| Go 1.27 API differs from cheat sheet | Compile/doc failure | Verify with local `go doc`; use established equivalent stdlib API |
| Demo timing is flaky | Scenario assertions vary | Relative schedule, explicit thresholds, three consecutive rehearsals |
| Reproducible hashes differ | Build proof fails | Pin env, add `-trimpath -buildvcs=false`, avoid build timestamps, inspect binary metadata |
| Body buffering consumes memory | Allocation/profile spike | Enforce body and concurrency caps; streaming stays later |
| Documentation lags code | Team cannot explain behavior | Update STDLIB/limits at each gate |

## Definition of done for every component

- Purpose and ownership are clear.
- Inputs, outputs, and limits are explicit.
- Errors are typed/classified.
- P0 tests pass.
- Concurrency ownership is documented.
- No third-party import is introduced.
- STDLIB substitution is recorded if relevant.
- Demo path or supporting role is known.
- Known limitations are written down.

## Final submission checklist

- [ ] Public repository.
- [ ] OSI-approved license.
- [ ] Go 1.27 stated.
- [ ] `go.mod` has no `require`.
- [ ] One-command build succeeds from a clean checkout.
- [ ] Tests and race detector results recorded.
- [ ] Dependency proof generated.
- [ ] README includes use case, commands, architecture, concurrency, security, limits, and benchmarks.
- [ ] STDLIB has at least ten real substitutions.
- [ ] Package Killer comparison is precise.
- [ ] Reproducible hashes match if claimed.
- [ ] Single implementation file if claimed.
- [ ] `.zero-dep.toml` identifies Track C and the one-line pitch.
- [ ] Five-minute video accessible.
- [ ] Demo and receipt use the submitted binary.
- [ ] No secret, absolute private path, temporary binary, or toolchain directory is committed.
