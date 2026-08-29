# Anvil — Five-Minute Demo Script

## Goal

The viewer should understand one story without reading the repository:

> Anvil receives real HTTP traffic through a hand-built protocol engine, detects a backend brownout and failure, routes around it, explains every decision, and produces measurable evidence—all from one zero-dependency binary.

The main demo is offline and self-contained. Record a clean primary take and a backup take.

## Pre-recording checklist

- Clean terminal font and zoom.
- Notifications disabled.
- Browser opened to the loopback admin dashboard.
- Repository at the final commit with no secrets or unrelated files.
- Go version and dependency proof already generated.
- Demo scenario tested three consecutive times.
- Dashboard labels readable at video resolution.
- No claim depends on an internet connection.
- Final benchmark numbers copied from the exact recorded build.

## Timeline and narration

### 0:00–0:25 — Hook

**Visual:** Dashboard topology with two healthy backend nodes and live traffic.

**Narration:**

“This is Anvil: a zero-dependency HTTP resilience lab. It is one Go binary containing a raw HTTP/1.1 server and client, reverse proxy, load balancer, circuit breaker, live traffic inspector, chaos runner, and benchmark engine. We are going to break a backend while traffic is live—and Anvil will keep serving and explain every decision.”

### 0:25–0:55 — Prove zero dependency and buildability

**Visual:** `go.mod`, dependency proof, build command, resulting binary.

**Actions:**

1. Show `go.mod` with no `require` block.
2. Show the final `deps-proof.txt` summary.
3. Run or display the one-command build.
4. Show the Go 1.27 version.

**Narration:**

“The module has no requirements. The dependency audit shows only our own module and Go’s standard library. This artifact builds in one command with Go 1.27.”

### 0:55–1:30 — Show protocol craft

**Visual:** Brief code view of the parser state/framing functions and a focused test result.

**Actions:**

- Show the test that feeds an HTTP request one byte at a time.
- Show the ambiguous `Transfer-Encoding` plus `Content-Length` rejection test.
- Show a real client request through Anvil.

**Narration:**

“The production data path does not use `net/http`. TCP is treated as a byte stream, so the same request parses correctly whether it arrives whole or one byte at a time. Ambiguous framing is rejected rather than guessed, preventing request-smuggling disagreement.”

Do not spend more than 35 seconds touring code.

### 1:30–2:00 — Establish normal traffic

**Visual:** `anvil demo` or `anvil experiment` starts fixtures and load; dashboard shows balanced healthy traffic.

**Narration:**

“Two in-process fixtures are receiving traffic through round robin selection. The fixtures use the same HTTP engine, so the entire demonstration remains offline and self-contained.”

Point out requests per second, p95 latency, backend state, and in-flight counts.

### 2:00–3:05 — The failure sequence

**Visual:** Timeline, topology, latency chart, and success counter.

**Scenario:**

1. Introduce latency on `node-b`.
2. Passive latency/failure evidence marks it suspect or contributes to the trip rule.
3. Make `node-b` unavailable or return configured failures.
4. Circuit opens and normal traffic excludes it.
5. Other nodes continue serving.
6. Recover `node-b`.
7. Cooldown expires; a bounded half-open probe succeeds.
8. The node returns to rotation.

**Narration:**

“The active health endpoint alone does not tell the whole story. Anvil also observes real request latency and failures. Here `node-b` degrades, its circuit opens, and selection immediately excludes it. Notice that the dashboard is not controlling the data plane; it is consuming the same bounded event ledger. After cooldown, Anvil permits a limited probe and restores the backend only after success.”

Avoid claiming zero errors unless the measured experiment actually shows zero errors.

### 3:05–3:40 — Explain a request

**Visual:** Select or highlight a ledger sequence and corresponding response diagnostics.

**Narration:**

“Each decision has a request ID, backend alias, reason code, and monotonic event sequence. Anvil also emits the standardized `Proxy-Status` header, so clients can distinguish a backend timeout, refusal, protocol error, or unavailable destination without scraping logs.”

Show one healthy response and one Anvil-generated gateway error if the scenario produces one safely.

### 3:40–4:15 — Resilience receipt

**Visual:** Final text/JSON receipt.

Call out:

- Scenario/configuration hash.
- Seed and resolved schedule.
- Total requests.
- Success rate.
- Maximum consecutive failures.
- Observed failover duration.
- Observed recovery duration.
- Estimated benchmark p95.
- Benchmark/ledger reconciliation.
- Passed/failed assertions.

**Narration:**

“This is not a certification. It is a reproducible experiment report derived from the same ledger events. The seed and configuration hash identify the conditions, and the assertions make success or failure explicit.”

### 4:15–4:40 — Package Killer and standard-library craft

**Visual:** `STDLIB.md` and circuit state-machine tests.

**Narration:**

“The flagship Package Killer is the circuit-breaker behavior commonly imported from `sony/gobreaker`, implemented here with `sync`, atomics, and time. We claim the breaker behavior Anvil needs, not API compatibility. The STDLIB log documents every shipped replacement, including routing, metrics, SSE, configuration, retries, IDs, and testing.”

Do not claim full API compatibility unless implemented and tested.

### 4:40–5:00 — Close

**Visual:** Matching reproducible-build hashes, green tests, dashboard topology healthy.

**Narration:**

“Anvil is intentionally HTTP/1.1-only and starts with bounded body buffering; those limits are documented. Within that scope it is testable, useful, explainable, and dependency-free. Break the backend. Keep the edge. Explain every decision.”

## Final Phase 9 rehearsal commands

```text
go version
go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...
go list -m all
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o anvil.exe .
go test -run TestOfflineFailureRecoveryExperimentThreeConsecutiveRuns -v ./...
.\verify-repro.ps1
.\anvil.exe demo --json-out receipt.json
.\anvil.exe experiment --scenario examples/failure-recovery.json --json-out receipt.json
.\anvil.exe bench --target 127.0.0.1:8080 --requests 1000 --concurrency 8
```

Never record commands that differ from README instructions.

## Phase 8 hardening evidence

Before recording the final take, run the exact test/race/fuzz/benchmark commands in `HARDENING.md`. For the compatibility proof, start local `dev-http` and `dev-proxy` processes, then show:

```text
curl --http1.1 http://127.0.0.1:8080/hello/anvil
curl --http1.1 -X POST --data-binary fixed-body http://127.0.0.1:8080/echo
curl --http1.1 -X POST -H "Transfer-Encoding: chunked" --data-binary chunked-body http://127.0.0.1:8080/echo
```

Open the loopback dashboard in a real browser and verify live metrics, backend topology, the causal timeline, and an empty error console. Do not quote machine-local benchmark values as portable guarantees.

## Backup plan

If the live browser rendering fails:

- Use the CLI event timeline and text metrics generated by the same ledger.
- Show the previously generated receipt from the exact build.
- Continue the scenario; do not switch to an unrelated prerecorded architecture animation.

If a fault timing varies:

- State the configured threshold.
- Let the experiment finish.
- Report measured timing rather than narrating a predetermined number.

The Single File bonus is not claimed. Do not describe the executable as a single source file; “one binary” is accurate.

## Claims requiring evidence

| Claim | Required evidence |
|---|---|
| Zero dependencies | go.mod, module audit, dependency proof |
| Raw HTTP engine | Production imports/code and protocol tests |
| Concurrent | Slow-client and load tests; documented model |
| Failover | Recorded scenario and ledger |
| Explainable | Reason-coded events and Proxy-Status |
| Reproducible | Two matching SHA-256 hashes |
| Package Killer | Implemented semantics, tests, honest comparison |
| Single file | Not claimed; 27 componentized production Go files are disclosed in README |
