# Anvil

Anvil is an explainable reverse proxy and resilience-testing lab built for Track C of the Zero Dependency Hackathon. Its core promise is to make every routing, failover, circuit-breaker, and recovery decision observable and reproducible from one offline-capable binary.

> Phase 0 status: repository foundation, command surface, configuration foundation, and concurrent raw-TCP echo proof are implemented. The reverse proxy and resilience features are intentionally not claimed yet.

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

These product commands are registered in Phase 0 and currently fail with an explicit status instead of pretending unfinished functionality exists.

The Phase 0 networking proof can be started with:

```sh
anvil dev-echo --listen 127.0.0.1:8080
```

It accepts raw TCP clients concurrently, echoes bytes without an HTTP framework, bounds admitted connections, and applies a per-connection deadline. It will be replaced by Anvil's raw HTTP/1.1 connection state machine in the next phases.

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

Phase 0 is not a reverse proxy. It does not yet parse HTTP, route requests, balance upstreams, run health checks, open circuits, publish telemetry, or execute experiments. These are acceptance-gated phases, and the README will be updated only when each capability is working and tested.

## License

MIT. See `LICENSE`.
