# Anvil — Coding-Agent Prompt Pack

## How to use this pack

Use the base contract at the start of every coding-agent session, then append exactly one phase prompt. Do not ask an agent to build the whole product in one turn. A phase is complete only when its acceptance checks pass.

Before every phase, give the agent the current repository state and the latest handoff summary. After every phase, require a concise report of changes, tests, risks, and the next smallest step.

## Base contract — prepend to every implementation prompt

```text
You are implementing Anvil for the Zero Dependency 72-hour hackathon, Track C.

Product: Anvil is an explainable HTTP/1.1 reverse proxy and resilience-testing lab. Its core differentiators are a raw TCP HTTP engine, load balancing, active/passive health, a closed/open/half-open circuit breaker, a causal decision ledger, standards-based Proxy-Status diagnostics, deterministic failure experiments, an integrated benchmark client, and resilience receipts.

Hard compliance rules:
1. Go 1.27 standard library only.
2. Do not add any require directive or third-party import.
3. Do not copy source from another package or online implementation.
4. Production HTTP parsing, server, client, router, proxy, SSE, and fixtures must not use net/http or net/http/httputil.
5. net/http and httptest may be used only in tests as independent compatibility oracles.
6. Do not shell out to separately installed runtime tools.
7. Keep the core demo offline and self-contained.
8. Preserve documented protocol invariants and resource limits.
9. Do not claim production readiness, zero allocation, specific scale, or package parity without evidence.
10. Do not implement stretch features until all mandatory gates pass.

Engineering rules:
- Inspect all current code and planning documents before editing.
- Preserve existing user/team changes and avoid unrelated rewrites.
- Explain the proposed design and affected invariants before implementation.
- Implement the smallest complete step for this phase.
- Use explicit state machines and typed/classified errors.
- Treat TCP as an arbitrary byte stream; never assume one read equals one message.
- Keep network I/O out of critical sections.
- Bound all client-controlled sizes, waits, goroutines, and queues.
- Use concrete types by default and narrow interfaces only at useful test seams.
- Add table-driven tests with every behavior.
- Run gofmt, go test, and relevant focused/race/fuzz checks.
- If a Go 1.27 API is unfamiliar, verify it with local go doc before using it.
- If a requirement conflicts with PROTOCOL_INVARIANTS.md, stop and report the conflict.

At the end report:
1. Files/sections changed.
2. Behavior implemented.
3. Tests added and exact commands/results.
4. Protocol or concurrency invariants affected.
5. Known limitations or risks.
6. The next smallest safe step.
```

## Prompt 0 — repository and compliance skeleton

```text
Implement only the post-kickoff repository skeleton and compliance foundation.

Required outcomes:
- Confirm the Go 1.27 executable and environment.
- Create go.mod with no require block.
- Establish package main and explicit CLI subcommand dispatch placeholders for proxy, demo, experiment, and bench.
- Add a minimal OSI-approved license and concise README/STDLIB placeholders if they do not exist.
- Establish a test file and a dependency-audit command note.
- Ensure the program compiles and help/invalid-command exit behavior is sensible.

Do not implement HTTP, proxying, dashboard, or fixtures in this phase.
Do not add generated boilerplate or dependencies.

Acceptance:
- gofmt succeeds.
- go test succeeds.
- go build succeeds.
- go.mod contains no require block.
- go list -m all shows only the project module.
```

## Prompt 1 — raw concurrent TCP foundation

```text
Implement the smallest raw TCP foundation needed by Anvil.

Required behavior:
- net.Listener accept loop.
- One goroutine per admitted connection.
- Configurable/global connection limit.
- Per-connection read/write deadlines.
- Graceful listener shutdown and goroutine join path.
- A temporary internal echo/test handler proving arbitrary byte round trips.

Tests:
- Multiple concurrent clients.
- One deliberately slow client does not block normal clients.
- Admission saturation remains bounded.
- Shutdown completes within a test bound.

Do not begin HTTP parsing until these tests pass. Keep the echo behavior isolated so it can be replaced cleanly.
```

## Prompt 2 — HTTP request codec

```text
Implement the HTTP/1.1 request model and incremental parser over bufio.Reader.

Read PROTOCOL_INVARIANTS.md sections 1–5 before editing.

Required behavior:
- Request line and HTTP/1.1 validation.
- Ordered duplicate-preserving header representation.
- Case-insensitive lookup without losing original field data.
- Host validation.
- Configurable request-line, header-byte, and header-count limits.
- Body framing decision: none, Content-Length, or chunked.
- Fixed body and chunked decoding with decoded-size limits.
- Strict Transfer-Encoding/Content-Length ambiguity handling.
- Preserve bytes already buffered for the next request.
- Typed protocol failures suitable for HTTP status mapping.

Tests must cover contiguous, one-byte, and arbitrary-fragmented input, multiple requests in one buffer, invalid headers, conflicting lengths, chunk edges, premature EOF, and limits. Add a fuzz target seeded with valid and malformed examples.

Do not add routing, proxying, or application handlers in this phase.
```

## Prompt 3 — response codec and connection lifecycle

```text
Implement the response model, serializer, chunked writer, and sequential HTTP/1.1 connection loop.

Required behavior:
- Correct status line and CRLF serialization.
- Header-value validation against response splitting.
- Accurate Content-Length for fixed responses.
- Valid chunked coding for streaming responses.
- Correct no-body rules for HEAD, 1xx, 204, and 304.
- HTTP/1.1 keep-alive by default and explicit close handling.
- Parse error to response mapping where a safe response is possible.
- Panic/handler recovery to a bounded 500 without process termination.
- Unsupported Upgrade, CONNECT, HTTP/2 preface, and Expect behavior exactly as documented.

Acceptance:
- A Go net/http client can complete multiple sequential requests over the raw server.
- Chunked responses decode correctly in the compatibility client.
- Slow/disconnected clients do not leak goroutines.
- All codec P0 tests pass.
```

## Prompt 4 — method-aware route tree

```text
Implement the immutable method-aware route tree used by Anvil.

Required behavior:
- Static segments, :named parameters, and terminal *wildcards.
- Static > parameter > wildcard precedence.
- Method-specific routes and an explicit any-method proxy route form.
- Query strings excluded from path matching.
- One documented percent-decoding policy; never double-decode.
- Duplicate/ambiguous registration failure before serving.
- Lock-free lookups after construction.

Keep the implementation proportional to Anvil's needs. Begin with a segment trie; do not add compressed radix edges unless tests and benchmarks justify them.

Add table-driven precedence, parameter, wildcard, ambiguity, and concurrent-read tests.
```

## Prompt 5 — reverse proxy vertical slice

```text
Implement a correct bounded-buffer reverse proxy vertical slice using Anvil's own client/server codec.

Read PROTOCOL_INVARIANTS.md sections 6, 8, 9, and 10 first.

Required behavior:
- Route to a configured backend pool.
- Dial with timeout.
- Reconstruct upstream requests from parsed structures.
- Parse Connection and remove nominated and known hop-by-hop fields.
- Rewrite Host/authority according to route configuration.
- Add Via and a request ID.
- Forward bounded request bodies.
- Parse upstream responses with the same codec rules.
- Preserve unknown end-to-end fields.
- Produce typed 502/503/504 errors consistently.
- Track downstream response commitment.
- Never retry yet; only expose enough attempt state for the next phase.

Create in-process backend fixtures using Anvil's own server engine. Use net/http only in tests as an independent client oracle.

Acceptance:
- Real client → Anvil → two fixtures succeeds for fixed and chunked messages.
- Hop-by-hop sanitation tests pass.
- Refusal, timeout, invalid response, body cap, and disconnect tests pass.
```

## Prompt 6 — balancing, health, retry, and circuit breaker

```text
Implement Anvil's resilience core without changing the HTTP codec.

Required behavior:
- Immutable backend identity/config plus concurrency-safe runtime state.
- Round-robin and least-in-flight selectors.
- Active HTTP health checks using Anvil's own client codec.
- Passive outcome classification for refusal, timeout, incomplete response, configured statuses, and latency.
- Closed/open/half-open circuit breaker with cooldown, bounded probes, success/failure counts, interval reset, and state callbacks.
- Backend-local state synchronization; no lock during network I/O.
- Safe bounded retries for GET/HEAD only, before downstream commitment, with a fully replayable buffered body.
- No automatic POST retry.
- Proxy-Status mapping and backend aliases.
- Exactly-once release of every in-flight reservation.

Use testing/synctest or a narrow Clock seam for deterministic timing tests. Add concurrent transition and race tests.

The flagship Package Killer target is the relevant behavior of sony/gobreaker. Do not claim full compatibility; record supported semantics and differences.
```

## Prompt 7 — ledger, metrics, SSE, and dashboard

```text
Implement causal observability without allowing it to block the data plane.

Required behavior:
- Structured metadata event type with monotonic sequence and reason codes.
- Fixed-capacity ledger.
- Atomic counters and fixed latency buckets.
- Backend/circuit snapshots.
- Selected runtime/metrics values.
- Bounded non-blocking subscriber queues and drop counters.
- Loopback-only administration listener by default.
- JSON metrics endpoint.
- Standards-valid SSE endpoint using Anvil's chunked writer, event IDs, heartbeat comments, and bounded replay from Last-Event-ID.
- Inline single-file-compatible HTML/CSS/vanilla-JS dashboard.
- Topology, request rate, status/errors, p50/p95/p99 estimates, in-flight counts, and decision timeline.

Privacy requirements:
- No bodies, Authorization, Cookie, Set-Cookie, or raw private backend addresses in events.
- Do not expose experiment mutation routes on the public listener.

Acceptance includes a slow subscriber test proving proxy traffic remains responsive.
```

## Prompt 8 — experiment runner, benchmark, and receipt

```text
Implement the self-contained Anvil product story.

Required behavior:
- In-process fixture backends using Anvil's HTTP server engine.
- Atomic fixture modes: healthy, delayed, configured failure, truncated response, and recovered/unavailable behavior.
- JSON scenario parser and strict validation.
- Relative deterministic schedule identified by seed and canonical configuration hash.
- Integrated benchmark workers using Anvil's request serializer and response parser.
- Bounded concurrency and duration, cancellation, keep-alive reuse where supported, error classes, status counts, transferred bytes, and fixed-bucket latency results.
- Experiment assertions for success rate, maximum failure streak, failover time, and recovery.
- Human-readable and JSON resilience receipts derived from ledger events.
- Non-zero exit status when assertions fail.
- `anvil demo`, `anvil experiment`, and `anvil bench` command integration.

Do not depend on internet access, public DNS, external backends, or separately installed load tools.

Acceptance:
- Three consecutive offline failure/recovery runs complete.
- Receipt values reconcile with ledger events.
- Workers/goroutines terminate after cancellation.
```

## Prompt 9 — protocol and concurrency hardening

```text
Do not add product features. Audit and harden the mandatory system.

Tasks:
- Execute every P0 test in TEST_MATRIX.md.
- Expand parser fuzz seeds from every discovered failure.
- Run the race detector.
- Test Slowloris, slow upstream, connection refusal, incomplete body, invalid upstream response, slow SSE subscriber, admission saturation, and shutdown.
- Audit every client-controlled length before allocation.
- Audit every goroutine for cancellation/join ownership.
- Audit every mutex to ensure no network I/O or observer send occurs while held.
- Audit sensitive-data exclusion from logs/events.
- Check consistent status, Proxy-Status, ledger reason, and metric mapping.
- Profile allocations and optimize only measured hot paths.

For each defect, first add the smallest failing regression test, then fix it. Report remaining limitations honestly.
```

## Prompt 10 — submission, dependency proof, and bonuses

```text
Prepare Anvil for submission without changing behavior unless a release-blocking defect is found.

Required work:
- Finalize README with problem, quick start, commands, architecture, concurrency model, protocol scope, security, limitations, benchmarks, and demo instructions.
- Convert STDLIB_DRAFT.md into an accurate STDLIB.md containing only shipped substitutions.
- Generate deps-proof.txt from the final checkout.
- Verify go.mod has no require block and go list -m all contains only Anvil.
- Add .zero-dep.toml with Track C and the final one-line pitch.
- Verify the OSI-approved license.
- Run clean build/test/race commands.
- Build twice on the same machine/toolchain with pinned flags and compare SHA-256 hashes.
- Assess Single File honestly. Consolidate only if tests remain green and readability is acceptable; otherwise remove the claim.
- Check that no toolchain, cache, secret, temporary binary, absolute path, or unrelated file is tracked.
- Produce the exact command list and evidence needed for the five-minute demo.

Do not invent benchmark results or mark a bonus complete without evidence.
```

## Reviewer prompt — independent audit

```text
Review the current Anvil repository against PROJECT_SPEC.md, ARCHITECTURE.md, PROTOCOL_INVARIANTS.md, and TEST_MATRIX.md. Do not edit initially.

Prioritize findings by:
P0: compliance violation, unsafe framing, request smuggling, data race, unbounded resource, retry duplication, incorrect failover, secret exposure, build failure.
P1: protocol incompatibility, misleading metric/receipt, lifecycle leak, incorrect error mapping, undocumented limitation.
P2: maintainability, performance, dashboard, or documentation improvement.

For each finding provide exact file/section, triggering input or sequence, violated invariant, impact, and smallest regression test. Verify claims with commands where possible. Do not recommend a third-party dependency.
```

## Bug-fix prompt

```text
Fix only the supplied failing behavior.

1. Reproduce it with the smallest deterministic test.
2. Identify the violated invariant and root cause.
3. Make the narrowest implementation change.
4. Run the focused test, relevant component suite, full suite, and race test if concurrency is involved.
5. Report any behavior change or remaining risk.

Do not refactor unrelated code or add dependencies while fixing the bug.
```

## Handoff template

```text
Phase:
Objective completed:
Files/sections changed:
Key types/functions:
Invariants implemented or affected:
Tests added:
Commands and results:
Known limitations:
Open risks:
Next smallest safe step:
```

## Disallowed agent shortcuts

- Replacing the custom engine with `net/http`, `httputil.ReverseProxy`, or a copied implementation.
- Reading once and assuming a complete request.
- Storing headers only in `map[string]string` before duplicate/framing validation.
- Modifying the first TCP chunk to inject proxy headers.
- Retrying after downstream response commitment.
- Retrying unsafe methods by default.
- Sleeping in tests when deterministic time is possible.
- Using unbounded goroutines, channels, bodies, headers, ledgers, or benchmark workers.
- Putting credentials or bodies into telemetry.
- Adding a package to save time.
- Implementing DNS/TLS/static/diagnostic stretch work before mandatory gates pass.
- Claiming a bonus based on planned rather than shipped behavior.
