package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"
)

const (
	scenarioVersion       = 1
	maxScenarioFixtures   = 8
	maxScenarioSteps      = 128
	maxExperimentWorkers  = 64
	maxExperimentRequests = 100_000
	maxExperimentDuration = 10 * time.Minute
)

type scenario struct {
	Version        int                `json:"version"`
	Name           string             `json:"name"`
	Seed           int64              `json:"seed"`
	DurationMS     int                `json:"duration_ms"`
	Fixtures       []fixtureSpec      `json:"fixtures"`
	Steps          []scenarioStep     `json:"steps"`
	Load           scenarioLoad       `json:"load"`
	Resilience     scenarioResilience `json:"resilience"`
	Assertions     scenarioAssertions `json:"assertions"`
	LedgerCapacity int                `json:"ledger_capacity"`
}

type fixtureSpec struct {
	Alias         string      `json:"alias"`
	InitialMode   fixtureMode `json:"initial_mode"`
	DelayMS       int         `json:"delay_ms,omitempty"`
	FailureStatus int         `json:"failure_status,omitempty"`
}

type scenarioStep struct {
	AtMS          int         `json:"at_ms"`
	JitterMS      int         `json:"jitter_ms,omitempty"`
	Fixture       string      `json:"fixture"`
	Mode          fixtureMode `json:"mode"`
	DelayMS       int         `json:"delay_ms,omitempty"`
	FailureStatus int         `json:"failure_status,omitempty"`
}

type scenarioLoad struct {
	Workers       int    `json:"workers"`
	Requests      int    `json:"requests"`
	RatePerSecond int    `json:"rate_per_second,omitempty"`
	Path          string `json:"path"`
	TimeoutMS     int    `json:"timeout_ms"`
}

type scenarioResilience struct {
	MaxAttempts       int `json:"max_attempts"`
	RetryTimeoutMS    int `json:"retry_timeout_ms"`
	PassiveFailures   int `json:"passive_failures"`
	PassiveIntervalMS int `json:"passive_interval_ms"`
	CircuitCooldownMS int `json:"circuit_cooldown_ms"`
	HalfOpenSuccesses int `json:"half_open_successes"`
	HealthIntervalMS  int `json:"health_interval_ms"`
	HealthTimeoutMS   int `json:"health_timeout_ms"`
	HealthFailures    int `json:"health_failures"`
	HealthSuccesses   int `json:"health_successes"`
}

type scenarioAssertions struct {
	MinimumSuccessRate float64 `json:"minimum_success_rate"`
	MaximumFailureRun  int     `json:"maximum_failure_streak"`
	MaximumFailoverMS  int     `json:"maximum_failover_ms"`
	RequireRecovery    bool    `json:"require_recovery"`
}

type scheduledStep struct {
	Index      int
	At         time.Duration
	Configured scenarioStep
}

func defaultDemoScenario() scenario {
	return scenario{
		Version:    scenarioVersion,
		Name:       "anvil-offline-failure-recovery",
		Seed:       4107,
		DurationMS: 6_000,
		Fixtures: []fixtureSpec{
			{Alias: "forge-a", InitialMode: fixtureHealthy},
			{Alias: "forge-b", InitialMode: fixtureHealthy},
		},
		Steps: []scenarioStep{
			{AtMS: 1_500, Fixture: "forge-a", Mode: fixtureUnavailable},
			{AtMS: 3_500, Fixture: "forge-a", Mode: fixtureRecovered},
		},
		Load: scenarioLoad{Workers: 4, Requests: 120, RatePerSecond: 20, Path: "/work", TimeoutMS: 1_000},
		Resilience: scenarioResilience{
			MaxAttempts: 2, RetryTimeoutMS: 750, PassiveFailures: 1, PassiveIntervalMS: 5_000,
			CircuitCooldownMS: 500, HalfOpenSuccesses: 1, HealthIntervalMS: 200,
			HealthTimeoutMS: 100, HealthFailures: 1, HealthSuccesses: 1,
		},
		Assertions:     scenarioAssertions{MinimumSuccessRate: 0.95, MaximumFailureRun: 2, MaximumFailoverMS: 1_000, RequireRecovery: true},
		LedgerCapacity: 2_048,
	}
}

func parseScenario(data []byte) (scenario, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value scenario
	if err := decoder.Decode(&value); err != nil {
		return scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return scenario{}, fmt.Errorf("decode scenario: multiple JSON values are not allowed")
		}
		return scenario{}, fmt.Errorf("decode scenario trailing data: %w", err)
	}
	if err := value.validate(); err != nil {
		return scenario{}, err
	}
	return value, nil
}

func (s scenario) validate() error {
	if s.Version != scenarioVersion {
		return fmt.Errorf("scenario version must be %d", scenarioVersion)
	}
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 128 {
		return fmt.Errorf("scenario name must contain 1 to 128 characters")
	}
	duration := time.Duration(s.DurationMS) * time.Millisecond
	if duration < 100*time.Millisecond || duration > maxExperimentDuration {
		return fmt.Errorf("scenario duration must be between 100ms and %s", maxExperimentDuration)
	}
	if len(s.Fixtures) < 2 || len(s.Fixtures) > maxScenarioFixtures {
		return fmt.Errorf("scenario requires 2 to %d fixtures", maxScenarioFixtures)
	}
	aliases := make(map[string]struct{}, len(s.Fixtures))
	for index, fixture := range s.Fixtures {
		if !validToken([]byte(fixture.Alias)) {
			return fmt.Errorf("fixture %d alias must be an HTTP token", index)
		}
		key := strings.ToLower(fixture.Alias)
		if _, exists := aliases[key]; exists {
			return fmt.Errorf("fixture alias %q is duplicated", fixture.Alias)
		}
		aliases[key] = struct{}{}
		if err := validateFixtureProfile(fixture.InitialMode, fixture.DelayMS, fixture.FailureStatus); err != nil {
			return fmt.Errorf("fixture %q: %w", fixture.Alias, err)
		}
		if fixture.InitialMode == fixtureRecovered {
			return fmt.Errorf("fixture %q cannot start in recovered mode", fixture.Alias)
		}
	}
	if len(s.Steps) == 0 || len(s.Steps) > maxScenarioSteps {
		return fmt.Errorf("scenario requires 1 to %d steps", maxScenarioSteps)
	}
	for index, step := range s.Steps {
		if step.AtMS < 0 || step.JitterMS < 0 || step.JitterMS > step.AtMS || step.AtMS+step.JitterMS >= s.DurationMS {
			return fmt.Errorf("step %d must resolve within the scenario duration", index)
		}
		if _, exists := aliases[strings.ToLower(step.Fixture)]; !exists {
			return fmt.Errorf("step %d references unknown fixture %q", index, step.Fixture)
		}
		if err := validateFixtureProfile(step.Mode, step.DelayMS, step.FailureStatus); err != nil {
			return fmt.Errorf("step %d: %w", index, err)
		}
	}
	resolvedModes := make(map[string]fixtureMode, len(s.Fixtures))
	for _, fixture := range s.Fixtures {
		resolvedModes[strings.ToLower(fixture.Alias)] = fixture.InitialMode
	}
	hasFault, hasRecovery := false, false
	for _, scheduled := range s.schedule() {
		key := strings.ToLower(scheduled.Configured.Fixture)
		if scheduled.Configured.Mode == fixtureRecovered && !isFaultMode(resolvedModes[key]) {
			return fmt.Errorf("step %d recovers fixture %q before a fault", scheduled.Index, scheduled.Configured.Fixture)
		}
		if isFaultMode(scheduled.Configured.Mode) {
			hasFault = true
		}
		if scheduled.Configured.Mode == fixtureRecovered {
			hasRecovery = true
		}
		resolvedModes[key] = scheduled.Configured.Mode
	}
	if !hasFault || (s.Assertions.RequireRecovery && !hasRecovery) {
		return fmt.Errorf("scenario must contain a fault and every required recovery")
	}
	if s.Load.Workers <= 0 || s.Load.Workers > maxExperimentWorkers {
		return fmt.Errorf("load workers must be between 1 and %d", maxExperimentWorkers)
	}
	if s.Load.Requests <= 0 || s.Load.Requests > maxExperimentRequests {
		return fmt.Errorf("load requests must be between 1 and %d", maxExperimentRequests)
	}
	if s.Load.RatePerSecond < 0 || s.Load.RatePerSecond > 10_000 {
		return fmt.Errorf("load rate must be between 0 and 10000 requests per second")
	}
	if !validScenarioPath(s.Load.Path) || s.Load.TimeoutMS <= 0 || s.Load.TimeoutMS > 60_000 {
		return fmt.Errorf("load path and timeout are invalid")
	}
	r := s.Resilience
	if r.MaxAttempts <= 0 || r.MaxAttempts > len(s.Fixtures) || r.RetryTimeoutMS <= 0 || r.PassiveFailures <= 0 || r.PassiveIntervalMS <= 0 || r.CircuitCooldownMS <= 0 || r.HalfOpenSuccesses <= 0 || r.HealthIntervalMS <= 0 || r.HealthTimeoutMS <= 0 || r.HealthTimeoutMS >= r.HealthIntervalMS || r.HealthFailures <= 0 || r.HealthSuccesses <= 0 {
		return fmt.Errorf("resilience values must be positive, bounded by fixtures, and health timeout must be below interval")
	}
	a := s.Assertions
	if a.MinimumSuccessRate < 0 || a.MinimumSuccessRate > 1 || a.MaximumFailureRun < 0 || a.MaximumFailoverMS <= 0 {
		return fmt.Errorf("assertion thresholds are invalid")
	}
	worstEvents := s.requiredLedgerCapacity()
	if s.LedgerCapacity < worstEvents || s.LedgerCapacity > 1_000_000 {
		return fmt.Errorf("ledger_capacity must be at least %d and at most 1000000", worstEvents)
	}
	return nil
}

func (s scenario) requiredLedgerCapacity() int {
	r := s.Resilience
	if r.HealthIntervalMS <= 0 || r.CircuitCooldownMS <= 0 {
		return max(s.Load.Requests*(2+r.MaxAttempts*3)+len(s.Steps)+32, 1)
	}
	healthTransitions := s.DurationMS / r.HealthIntervalMS
	circuitTransitions := s.DurationMS / r.CircuitCooldownMS
	return s.Load.Requests*(2+r.MaxAttempts*3) + len(s.Steps) + len(s.Fixtures)*(healthTransitions+circuitTransitions+4)
}

func validScenarioPath(path string) bool {
	return path != "" && path[0] == '/' && !strings.ContainsAny(path, "\r\n# ")
}

func (s scenario) canonicalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func (s scenario) canonicalHash() (string, error) {
	canonical, err := s.canonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func (s scenario) schedule() []scheduledStep {
	random := rand.New(rand.NewSource(s.Seed))
	steps := make([]scheduledStep, len(s.Steps))
	for index, step := range s.Steps {
		resolved := step.AtMS
		if step.JitterMS > 0 {
			resolved += random.Intn(step.JitterMS*2+1) - step.JitterMS
		}
		steps[index] = scheduledStep{Index: index, At: time.Duration(resolved) * time.Millisecond, Configured: step}
	}
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].At != steps[j].At {
			return steps[i].At < steps[j].At
		}
		return steps[i].Index < steps[j].Index
	})
	return steps
}
