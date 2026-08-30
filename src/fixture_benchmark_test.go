package main

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

func TestFixtureAtomicModesUseAnvilHTTPCodec(t *testing.T) {
	fixture, stop := startTestFixtureBackend(t, fixtureSpec{Alias: "mode-node", InitialMode: fixtureHealthy})
	defer stop()
	result := runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.StatusCounts["200"] != 1 {
		t.Fatalf("healthy result = %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureDelayed, Delay: 25 * time.Millisecond})
	result = runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.StatusCounts["200"] != 1 || result.Latency.P50MS < 25 {
		t.Fatalf("delayed result = %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureFailure, FailureStatus: 503})
	result = runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.StatusCounts["503"] != 1 {
		t.Fatalf("failure result = %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureTruncated})
	result = runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.ErrorCounts["protocol_incomplete_message"] != 1 {
		t.Fatalf("truncated result = %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureUnavailable})
	result = runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.Completed != 1 || len(result.ErrorCounts) == 0 {
		t.Fatalf("unavailable result = %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureRecovered})
	result = runFixtureBenchmark(t, fixture, 1, time.Second)
	if result.StatusCounts["200"] != 1 || fixture.snapshot().Mode != fixtureRecovered {
		t.Fatalf("recovered result = %+v snapshot=%+v", result, fixture.snapshot())
	}
}

func TestBenchCommandJSONUsesBoundedEngine(t *testing.T) {
	fixture, stop := startTestFixtureBackend(t, fixtureSpec{Alias: "cli-bench", InitialMode: fixtureHealthy})
	defer stop()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runBenchCommand([]string{"--target", fixture.address, "--requests", "12", "--concurrency", "3", "--duration-ms", "1000", "--json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("bench code=%d stderr=%q", code, stderr.String())
	}
	var result benchmarkResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("JSON result: %v; output=%q", err, stdout.String())
	}
	if result.Completed != 12 || result.StatusCounts["200"] != 12 || result.PeakInFlight > 3 {
		t.Fatalf("bench result = %+v", result)
	}
}

func TestBenchmarkBoundsReuseAccountingAndCancellation(t *testing.T) {
	fixture, stop := startTestFixtureBackend(t, fixtureSpec{Alias: "bench-node", InitialMode: fixtureHealthy})
	defer stop()
	result := runFixtureBenchmark(t, fixture, 40, 2*time.Second)
	if result.Completed != 40 || result.StatusCounts["200"] != 40 || result.PeakInFlight > 4 || result.PeakInFlight <= 0 {
		t.Fatalf("benchmark result = %+v", result)
	}
	if result.NewConnections == 0 || result.NewConnections > 4 || result.ReusedRequests == 0 || result.RequestBytes == 0 || result.ResponseBytes == 0 {
		t.Fatalf("connection/byte accounting = %+v", result)
	}
	if result.Completed != countValues(result.StatusCounts)+countValues(result.ErrorCounts) {
		t.Fatalf("result counts do not reconcile: %+v", result)
	}

	fixture.apply(fixtureProfile{Mode: fixtureDelayed, Delay: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan benchmarkResult, 1)
	go func() {
		value, _ := runBenchmark(ctx, benchmarkConfig{Address: fixture.address, Workers: 4, MaxRequests: 100, Duration: 5 * time.Second, Timeout: 2 * time.Second})
		done <- value
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for fixture.active.Load() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if fixture.active.Load() == 0 {
		t.Fatal("benchmark did not reach the delayed fixture")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("benchmark workers did not terminate promptly after cancellation")
	}
}

func startTestFixtureBackend(t *testing.T, spec fixtureSpec) (*fixtureBackend, func()) {
	t.Helper()
	fixture, err := newFixtureBackend(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.server.Serve(ctx) }()
	return fixture, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("fixture shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("fixture shutdown timed out")
		}
	}
}

func runFixtureBenchmark(t *testing.T, fixture *fixtureBackend, requests int, duration time.Duration) benchmarkResult {
	t.Helper()
	result, err := runBenchmark(context.Background(), benchmarkConfig{Address: fixture.address, Workers: 4, MaxRequests: requests, Duration: duration, Timeout: duration})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func countValues(values map[string]uint64) uint64 {
	var count uint64
	for _, value := range values {
		count += value
	}
	return count
}
