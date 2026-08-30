package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestOfflineFailureRecoveryExperimentThreeConsecutiveRuns(t *testing.T) {
	value := fastExperimentScenario()
	hash, err := value.canonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	for run := range 3 {
		lab, err := newExperimentLab(value)
		if err != nil {
			t.Fatalf("run %d initialize: %v", run, err)
		}
		receipt, runErr := lab.run(context.Background(), io.Discard)
		lab.close()
		if runErr != nil {
			t.Fatalf("run %d: %v", run, runErr)
		}
		if !receipt.Passed || receipt.ScenarioHash != hash || receipt.Requests != uint64(value.Load.Requests) || receipt.Benchmark.Completed != uint64(value.Load.Requests) || !receipt.BenchmarkReconciled {
			t.Fatalf("run %d receipt = %+v", run, receipt)
		}
		if len(receipt.Failovers) != 1 || !receipt.Failovers[0].Observed || len(receipt.Recoveries) != 1 || !receipt.Recoveries[0].Observed {
			t.Fatalf("run %d transitions: failovers=%+v recoveries=%+v", run, receipt.Failovers, receipt.Recoveries)
		}
		for _, fixture := range receipt.Fixtures {
			if fixture.Active != 0 {
				t.Fatalf("run %d fixture still active: %+v", run, fixture)
			}
		}
	}
}

func TestScenarioAssertionFailureControlsCommandExit(t *testing.T) {
	value := fastExperimentScenario()
	value.Steps = []scenarioStep{
		{AtMS: 100, Fixture: "forge-a", Mode: fixtureUnavailable},
		{AtMS: 100, Fixture: "forge-b", Mode: fixtureUnavailable},
		{AtMS: 700, Fixture: "forge-a", Mode: fixtureRecovered},
		{AtMS: 700, Fixture: "forge-b", Mode: fixtureRecovered},
	}
	value.Assertions.MinimumSuccessRate = 1
	value.Assertions.MaximumFailureRun = value.Load.Requests
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runScenarioCommand(value, false, "", &stdout, &stderr); code != exitFailure {
		t.Fatalf("exit code = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Anvil resilience receipt: FAIL") || !strings.Contains(stdout.String(), "[FAIL] minimum_success_rate") {
		t.Fatalf("failing receipt output = %q", stdout.String())
	}
}

func fastExperimentScenario() scenario {
	value := defaultDemoScenario()
	value.Name = "fast-offline-regression"
	value.DurationMS = 900
	value.Load.Requests = 30
	value.Load.RatePerSecond = 40
	value.Load.Workers = 4
	value.Load.TimeoutMS = 300
	value.Steps = []scenarioStep{
		{AtMS: 180, Fixture: "forge-a", Mode: fixtureUnavailable},
		{AtMS: 450, Fixture: "forge-a", Mode: fixtureRecovered},
	}
	value.Resilience.RetryTimeoutMS = 250
	value.Resilience.PassiveFailures = 1
	value.Resilience.PassiveIntervalMS = 2_000
	value.Resilience.CircuitCooldownMS = 80
	value.Resilience.HealthIntervalMS = 40
	value.Resilience.HealthTimeoutMS = 20
	value.Resilience.HealthFailures = 1
	value.Resilience.HealthSuccesses = 1
	value.Assertions.MinimumSuccessRate = 0.90
	value.Assertions.MaximumFailureRun = 2
	value.Assertions.MaximumFailoverMS = 300
	value.Assertions.RequireRecovery = true
	value.LedgerCapacity = 512
	return value
}
