# Anvil — Phase 9 Release Evidence

## Candidate identity

- Generated: 2026-08-30
- Track: C — Web & Network
- Toolchain: Go 1.27.0, windows/amd64, `CGO_ENABLED=0` for release builds
- Benchmark host: AMD Ryzen AI 7 350 with Radeon 860M
- Pitch: Every failover leaves a receipt: an explainable reverse proxy and resilience lab in one zero-dependency binary.
- Behavior freeze: Phase 9 adds no feature scope. It includes one narrow experiment-shutdown correction found by the final repeated gate, one deterministic test-oracle correction, and a tracked zero-dependency enforcement gate.

## Clean gate

| Gate | Exact command | Result |
|---|---|---|
| Formatting | `gofmt -l` over every Go file | PASS — no paths returned |
| Standard tests | `go test -count=3 ./...` | PASS — package completed in 26.911 s on the safeguard checkout |
| Static analysis | `go vet ./...` | PASS |
| Race detector | `go test -c -race -o anvil-race.test.exe .`, then `anvil-race.test.exe -test.count=3` using MinGW-w64 GCC | PASS for the unchanged Go source — Phase 9 recorded three complete race-enabled executions; the safeguard refresh compiled successfully with race instrumentation, while Windows Smart App Control blocked launching the newly generated unsigned executable |
| Enforced dependency boundary | `.\verify-zero-dep.ps1` | PASS — one module, 27 production Go files, 24 unique production imports, no production `net/http`, and no external dependency |
| Module graph | `go list -m all` | PASS — only `github.com/ayushman4107/Anvil---Zero-Dependency-Hackathon` |
| External imports | `go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./...` | PASS — empty output |
| Manifest | `go mod edit -json` | PASS — module and Go version only; no requirements |
| Reproducibility | `.\verify-repro.ps1` | PASS — matching hashes below |

Phase 8's four native fuzz targets, Slowloris/slow-upstream/admission/shutdown regressions, curl HTTP/1.1 compatibility, and real Chromium dashboard check remain recorded in `HARDENING.md`. Phase 9 does not change the HTTP codec, proxy transaction, breaker, observer, or dashboard code.

The enforcement script was also exercised against isolated negative fixtures. It rejected a `require` directive, a production `net/http` import, and a third-party production import. `verify-repro.ps1` invokes this gate before building, and the tracked `.githooks/pre-commit` invokes it before commits when `core.hooksPath` is enabled, so dependency drift does not rely on a manual audit.

## Release-blocker corrections found by the gate

1. `TestScenarioAssertionFailureControlsCommandExit` assumed a 1 ms maximum failover would always fail. One race run completed failover in 0.515 ms. The test now makes both fixtures unavailable for a bounded interval and requires 100% success, producing a deterministic failed assertion. It passed ten ordinary and ten race-enabled focused runs.
2. `TestOfflineFailureRecoveryExperimentThreeConsecutiveRuns` caught a fixture snapshot with `active=1`: response bytes had reached the proxy, but the fixture goroutine had not yet completed its final accounting. Experiment teardown now stops health workers, closes idle upstreams, cancels and joins all lab servers, and only then snapshots ledger/fixture state. It passed 30 ordinary and 15 race-enabled focused experiment runs before the full gates above.

## Reproducible release artifact

Pinned command used by `verify-repro.ps1`:

```text
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid="
```

```text
first_sha256 =  470A0FF8351CDA0BE2727E0B3E83B25C12DFDD44202078E7D3A849066C683974
second_sha256 = 470A0FF8351CDA0BE2727E0B3E83B25C12DFDD44202078E7D3A849066C683974
byte_for_byte_match = true
```

The submitted local `anvil.exe` was built directly from the candidate with the same flags and matched this hash before the demo runs.

## Exact submitted-binary demo evidence

All three runs used the binary identified above and scenario SHA-256 `1b54431082c4e688deef5d134801a41f9bf0bed4dfe8165917e50eb17275dc15`.

| Run | Requests | Successes | Failures | Failover | Recovery | Active fixtures at receipt | Ledger reconciled | Assertions |
|---:|---:|---:|---:|---:|---:|---:|---|---|
| 1 | 120 | 120 | 0 | 0.000 ms | 250.710 ms | 0 | yes | PASS |
| 2 | 120 | 120 | 0 | 1.096 ms | 250.348 ms | 0 | yes | PASS |
| 3 | 120 | 120 | 0 | 0.650 ms | 251.528 ms | 0 | yes | PASS |

These are controlled local measurements, not production service-level claims.

## Allocation evidence

Command:

```text
go test -run '^$' -bench 'Benchmark(ReadHTTPRequest|BackendReserveRoundRobin)$' -benchmem -benchtime=2s -count=5 .
```

| Benchmark | Five-run timing | Bytes/op | Allocations/op |
|---|---:|---:|---:|
| `BenchmarkReadHTTPRequest-16` | 2,343–2,671 ns; median 2,420 ns | 6,480 | 30 |
| `BenchmarkBackendReserveRoundRobin-16` | 127.6–134.4 ns; median 131.9 ns | 16 | 1 |

## Submission and bonus decisions

- **STDLIB Log — claimed.** `STDLIB.md` contains only shipped substitutions, an exact direct-import inventory, and honest boundaries.
- **Package Killer — claimed narrowly.** Anvil implements the proxy-integrated closed/open/half-open circuit-breaker behavior documented in `STDLIB.md`; it does not claim `sony/gobreaker` API compatibility.
- **Reproducible Build — claimed.** The checked-in verifier produced the matching hashes above and cleans its isolated temporary directory.
- **Single File — not claimed.** The 6,212 production lines remain in 27 component-owned Go files because a freeze-time merge would reduce auditability and team explainability.
- **License — verified.** The repository contains the standard MIT text, which the Open Source Initiative lists as an approved license.

## Five-minute recording command list

Run from a clean checkout with Go 1.27.0 in `PATH`:

```powershell
go version
Get-Content go.mod
Get-Content deps-proof.txt
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o anvil.exe .
.\verify-repro.ps1
.\anvil.exe demo --json-out receipt.json
```

Then show the causal timeline, one failover reason, one recovery transition, the final receipt assertions, `STDLIB.md`, and the matching hashes. `DEMO_SCRIPT.md` contains the timed narration and backup path.
