package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleScenarioMatchesBuiltInDemo(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "examples", "failure-recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseScenario(data)
	if err != nil {
		t.Fatal(err)
	}
	exampleHash, _ := parsed.canonicalHash()
	builtInHash, _ := defaultDemoScenario().canonicalHash()
	if exampleHash != builtInHash {
		t.Fatalf("example hash %s != built-in hash %s", exampleHash, builtInHash)
	}
}

func TestScenarioStrictParsingCanonicalHashAndSeededSchedule(t *testing.T) {
	value := defaultDemoScenario()
	value.Steps[0].JitterMS = 100
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseScenario(encoded)
	if err != nil {
		t.Fatal(err)
	}
	hashA, err := parsed.canonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(value, "", "  ")
	parsedPretty, err := parseScenario(pretty)
	if err != nil {
		t.Fatal(err)
	}
	hashB, _ := parsedPretty.canonicalHash()
	if hashA != hashB || len(hashA) != 64 {
		t.Fatalf("canonical hashes = %q and %q", hashA, hashB)
	}
	scheduleA, scheduleB := parsed.schedule(), parsed.schedule()
	if len(scheduleA) != len(scheduleB) {
		t.Fatal("same seed produced different schedule lengths")
	}
	for index := range scheduleA {
		if scheduleA[index].At != scheduleB[index].At || scheduleA[index].Index != scheduleB[index].Index {
			t.Fatalf("same seed produced different step %d", index)
		}
	}
}

func TestScenarioRejectsUnknownTrailingAndUnsafeValues(t *testing.T) {
	value := defaultDemoScenario()
	encoded, _ := json.Marshal(value)
	unknown := strings.Replace(string(encoded), `"version":1`, `"version":1,"surprise":true`, 1)
	if _, err := parseScenario([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := parseScenario(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	value.Load.Workers = maxExperimentWorkers + 1
	if err := value.validate(); err == nil {
		t.Fatal("unbounded workers were accepted")
	}
	value = defaultDemoScenario()
	value.LedgerCapacity = 1
	if err := value.validate(); err == nil || !strings.Contains(err.Error(), "ledger_capacity") {
		t.Fatalf("undersized ledger error = %v", err)
	}
}
