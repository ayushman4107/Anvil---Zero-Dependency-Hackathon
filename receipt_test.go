package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReceiptDerivesAssertionsFromLedger(t *testing.T) {
	value := defaultDemoScenario()
	value.Assertions = scenarioAssertions{MinimumSuccessRate: 0.70, MaximumFailureRun: 1, MaximumFailoverMS: 1, RequireRecovery: true}
	events := []decisionEvent{
		{Sequence: 1, ElapsedMicros: 100, Type: eventFixtureTransition, BackendAlias: "forge-a", PreviousState: "healthy", NewState: "unavailable"},
		{Sequence: 2, ElapsedMicros: 150, Type: eventRequestCompleted, BackendAlias: "forge-b", Status: 200},
		{Sequence: 3, ElapsedMicros: 200, Type: eventRequestCompleted, Status: 503},
		{Sequence: 4, ElapsedMicros: 250, Type: eventRequestCompleted, BackendAlias: "forge-b", Status: 200},
		{Sequence: 5, ElapsedMicros: 300, Type: eventFixtureTransition, BackendAlias: "forge-a", PreviousState: "unavailable", NewState: "recovered"},
		{Sequence: 6, ElapsedMicros: 400, Type: eventRequestCompleted, BackendAlias: "forge-a", Status: 200},
	}
	benchmark := benchmarkResult{Completed: 4, StatusCounts: map[string]uint64{"200": 3, "503": 1}, ErrorCounts: map[string]uint64{}}
	receipt := deriveReceipt(value, strings.Repeat("a", 64), ledgerSnapshot{Events: events}, benchmark, []fixtureSnapshot{{Alias: "forge-a", Address: "127.0.0.1:9999", Mode: fixtureRecovered}})
	if !receipt.Passed || receipt.Requests != 4 || receipt.Successes != 3 || receipt.Failures != 1 || receipt.MaximumFailureRun != 1 || !receipt.BenchmarkReconciled {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(receipt.Failovers) != 1 || !receipt.Failovers[0].Observed || receipt.Failovers[0].DurationMicros != 50 {
		t.Fatalf("failovers = %+v", receipt.Failovers)
	}
	if len(receipt.Recoveries) != 1 || !receipt.Recoveries[0].Observed || receipt.Recoveries[0].DurationMicros != 100 {
		t.Fatalf("recoveries = %+v", receipt.Recoveries)
	}
	encoded, err := marshalReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("127.0.0.1:9999")) || !bytes.Contains(encoded, []byte(`"passed": true`)) {
		t.Fatalf("receipt JSON privacy/schema = %s", encoded)
	}
	var human bytes.Buffer
	if err := writeHumanReceipt(&human, receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "Anvil resilience receipt: PASS") || !strings.Contains(human.String(), "Configuration SHA-256") {
		t.Fatalf("human receipt = %q", human.String())
	}
}

func TestReceiptFailureControlsPassState(t *testing.T) {
	value := defaultDemoScenario()
	value.Assertions.MinimumSuccessRate = 1
	value.Assertions.MaximumFailureRun = 0
	events := []decisionEvent{{Sequence: 1, Type: eventRequestCompleted, Status: 503}}
	receipt := deriveReceipt(value, "hash", ledgerSnapshot{Events: events}, benchmarkResult{Completed: 1, StatusCounts: map[string]uint64{"503": 1}}, nil)
	if receipt.Passed {
		t.Fatalf("failing receipt passed: %+v", receipt.Assertions)
	}
}
