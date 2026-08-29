package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type failoverMeasurement struct {
	Fixture        string `json:"fixture"`
	FaultSequence  uint64 `json:"fault_sequence"`
	Observed       bool   `json:"observed"`
	DurationMicros int64  `json:"duration_micros,omitempty"`
}

type recoveryMeasurement struct {
	Fixture          string `json:"fixture"`
	RecoverySequence uint64 `json:"recovery_sequence"`
	Observed         bool   `json:"observed"`
	DurationMicros   int64  `json:"duration_micros,omitempty"`
}

type assertionResult struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Passed   bool   `json:"passed"`
}

type receiptScheduleStep struct {
	AtMS    int64       `json:"at_ms"`
	Fixture string      `json:"fixture"`
	Mode    fixtureMode `json:"mode"`
}

type resilienceReceipt struct {
	SchemaVersion       int                   `json:"schema_version"`
	ScenarioName        string                `json:"scenario_name"`
	ScenarioHash        string                `json:"scenario_hash_sha256"`
	Seed                int64                 `json:"seed"`
	Schedule            []receiptScheduleStep `json:"resolved_schedule"`
	DurationMicros      int64                 `json:"duration_micros"`
	LedgerFirstSequence uint64                `json:"ledger_first_sequence"`
	LedgerLastSequence  uint64                `json:"ledger_last_sequence"`
	LedgerEventCount    int                   `json:"ledger_event_count"`
	EventCounts         map[string]uint64     `json:"event_counts"`
	Requests            uint64                `json:"requests"`
	Successes           uint64                `json:"successes"`
	Failures            uint64                `json:"failures"`
	SuccessRate         float64               `json:"success_rate"`
	MaximumFailureRun   uint64                `json:"maximum_failure_streak"`
	Failovers           []failoverMeasurement `json:"failovers"`
	Recoveries          []recoveryMeasurement `json:"recoveries"`
	Benchmark           benchmarkResult       `json:"benchmark"`
	BenchmarkReconciled bool                  `json:"benchmark_reconciled"`
	Fixtures            []fixtureSnapshot     `json:"fixtures"`
	Assertions          []assertionResult     `json:"assertions"`
	Passed              bool                  `json:"passed"`
}

func deriveReceipt(scenario scenario, hash string, ledger ledgerSnapshot, benchmark benchmarkResult, fixtures []fixtureSnapshot) resilienceReceipt {
	receipt := resilienceReceipt{
		SchemaVersion: 1, ScenarioName: scenario.Name, ScenarioHash: hash, Seed: scenario.Seed,
		EventCounts: make(map[string]uint64), Benchmark: benchmark, Fixtures: fixtures, Passed: true,
	}
	for _, step := range scenario.schedule() {
		receipt.Schedule = append(receipt.Schedule, receiptScheduleStep{AtMS: step.At.Milliseconds(), Fixture: step.Configured.Fixture, Mode: step.Configured.Mode})
	}
	if len(ledger.Events) != 0 {
		receipt.LedgerFirstSequence = ledger.Events[0].Sequence
		receipt.LedgerLastSequence = ledger.Events[len(ledger.Events)-1].Sequence
		receipt.DurationMicros = ledger.Events[len(ledger.Events)-1].ElapsedMicros
	}
	receipt.LedgerEventCount = len(ledger.Events)
	var failureRun uint64
	for _, event := range ledger.Events {
		receipt.EventCounts[string(event.Type)]++
		if event.Type != eventRequestCompleted {
			continue
		}
		receipt.Requests++
		if successfulStatus(event.Status) {
			receipt.Successes++
			failureRun = 0
		} else {
			receipt.Failures++
			failureRun++
			if failureRun > receipt.MaximumFailureRun {
				receipt.MaximumFailureRun = failureRun
			}
		}
	}
	if receipt.Requests != 0 {
		receipt.SuccessRate = float64(receipt.Successes) / float64(receipt.Requests)
	}
	receipt.Failovers = deriveFailovers(ledger.Events)
	receipt.Recoveries = deriveRecoveries(ledger.Events)
	receipt.BenchmarkReconciled = benchmark.Completed == receipt.Requests
	receipt.Assertions = evaluateAssertions(scenario.Assertions, receipt)
	for _, assertion := range receipt.Assertions {
		if !assertion.Passed {
			receipt.Passed = false
		}
	}
	return receipt
}

func successfulStatus(status int) bool { return status >= 200 && status < 400 }

func deriveFailovers(events []decisionEvent) []failoverMeasurement {
	var measurements []failoverMeasurement
	for index, event := range events {
		if event.Type != eventFixtureTransition || !isFaultMode(fixtureMode(event.NewState)) {
			continue
		}
		measurement := failoverMeasurement{Fixture: event.BackendAlias, FaultSequence: event.Sequence}
		for _, candidate := range events[index+1:] {
			if candidate.Type == eventRequestCompleted && successfulStatus(candidate.Status) && candidate.BackendAlias != "" && !strings.EqualFold(candidate.BackendAlias, event.BackendAlias) {
				measurement.Observed = true
				measurement.DurationMicros = max(candidate.ElapsedMicros-event.ElapsedMicros, 0)
				break
			}
		}
		measurements = append(measurements, measurement)
	}
	return measurements
}

func deriveRecoveries(events []decisionEvent) []recoveryMeasurement {
	var measurements []recoveryMeasurement
	for index, event := range events {
		if event.Type != eventFixtureTransition || fixtureMode(event.NewState) != fixtureRecovered {
			continue
		}
		measurement := recoveryMeasurement{Fixture: event.BackendAlias, RecoverySequence: event.Sequence}
		for _, candidate := range events[index+1:] {
			if candidate.Type == eventRequestCompleted && successfulStatus(candidate.Status) && strings.EqualFold(candidate.BackendAlias, event.BackendAlias) {
				measurement.Observed = true
				measurement.DurationMicros = max(candidate.ElapsedMicros-event.ElapsedMicros, 0)
				break
			}
		}
		measurements = append(measurements, measurement)
	}
	return measurements
}

func isFaultMode(mode fixtureMode) bool {
	return mode == fixtureDelayed || mode == fixtureFailure || mode == fixtureTruncated || mode == fixtureUnavailable
}

func evaluateAssertions(expected scenarioAssertions, receipt resilienceReceipt) []assertionResult {
	results := []assertionResult{
		{Name: "minimum_success_rate", Expected: fmt.Sprintf(">= %.4f", expected.MinimumSuccessRate), Actual: fmt.Sprintf("%.4f", receipt.SuccessRate), Passed: receipt.SuccessRate >= expected.MinimumSuccessRate},
		{Name: "maximum_failure_streak", Expected: fmt.Sprintf("<= %d", expected.MaximumFailureRun), Actual: fmt.Sprint(receipt.MaximumFailureRun), Passed: receipt.MaximumFailureRun <= uint64(expected.MaximumFailureRun)},
	}
	maxFailoverMicros := int64(0)
	failoverObserved := len(receipt.Failovers) != 0
	for _, value := range receipt.Failovers {
		failoverObserved = failoverObserved && value.Observed
		if value.DurationMicros > maxFailoverMicros {
			maxFailoverMicros = value.DurationMicros
		}
	}
	results = append(results, assertionResult{
		Name: "maximum_failover_time", Expected: fmt.Sprintf("observed and <= %dms", expected.MaximumFailoverMS),
		Actual: fmt.Sprintf("observed=%t duration=%.3fms", failoverObserved, float64(maxFailoverMicros)/1_000),
		Passed: failoverObserved && maxFailoverMicros <= int64(expected.MaximumFailoverMS)*1_000,
	})
	if expected.RequireRecovery {
		recoveryObserved := len(receipt.Recoveries) != 0
		for _, value := range receipt.Recoveries {
			recoveryObserved = recoveryObserved && value.Observed
		}
		results = append(results, assertionResult{Name: "recovery", Expected: "every recovery observed", Actual: fmt.Sprintf("observed=%t", recoveryObserved), Passed: recoveryObserved})
	}
	results = append(results, assertionResult{Name: "benchmark_ledger_reconciliation", Expected: "benchmark completed equals ledger completions", Actual: fmt.Sprintf("benchmark=%d ledger=%d", receipt.Benchmark.Completed, receipt.Requests), Passed: receipt.BenchmarkReconciled})
	return results
}

func writeHumanReceipt(writer io.Writer, receipt resilienceReceipt) error {
	result := "PASS"
	if !receipt.Passed {
		result = "FAIL"
	}
	if _, err := fmt.Fprintf(writer, "Anvil resilience receipt: %s\nScenario: %s\nSeed: %d\nConfiguration SHA-256: %s\nRequests: %d (success=%d failure=%d rate=%.2f%%)\nMaximum failure streak: %d\n", result, receipt.ScenarioName, receipt.Seed, receipt.ScenarioHash, receipt.Requests, receipt.Successes, receipt.Failures, receipt.SuccessRate*100, receipt.MaximumFailureRun); err != nil {
		return err
	}
	for _, value := range receipt.Failovers {
		fmt.Fprintf(writer, "Failover %s: observed=%t duration=%.3fms\n", value.Fixture, value.Observed, float64(value.DurationMicros)/1_000)
	}
	for _, value := range receipt.Recoveries {
		fmt.Fprintf(writer, "Recovery %s: observed=%t duration=%.3fms\n", value.Fixture, value.Observed, float64(value.DurationMicros)/1_000)
	}
	for _, assertion := range receipt.Assertions {
		status := "PASS"
		if !assertion.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(writer, "[%s] %s — expected %s; actual %s\n", status, assertion.Name, assertion.Expected, assertion.Actual)
	}
	keys := make([]string, 0, len(receipt.EventCounts))
	for key := range receipt.EventCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(writer, "event.%s=%d\n", key, receipt.EventCounts[key])
	}
	return nil
}

func marshalReceipt(receipt resilienceReceipt) ([]byte, error) {
	return json.MarshalIndent(receipt, "", "  ")
}
