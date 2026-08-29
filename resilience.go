package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultBackendInFlight         = 128
	defaultBackendIdleConnections  = 8
	defaultBackendIdleTimeout      = 30 * time.Second
	defaultPassiveFailureThreshold = 3
	defaultPassiveInterval         = 30 * time.Second
	defaultCircuitCooldown         = 2 * time.Second
	defaultHalfOpenMaxRequests     = 1
	defaultHalfOpenSuccesses       = 1
	defaultActiveFailureThreshold  = 2
	defaultActiveSuccessThreshold  = 2
)

type selectorPolicy string

const (
	selectorRoundRobin    selectorPolicy = "round-robin"
	selectorLeastInFlight selectorPolicy = "least-in-flight"
)

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitOpen     circuitState = "open"
	circuitHalfOpen circuitState = "half-open"
)

type circuitTransition struct {
	BackendAlias string
	From         circuitState
	To           circuitState
	At           time.Time
	Reason       string
}

type resilienceConfig struct {
	Selector                selectorPolicy
	PassiveFailureThreshold int
	PassiveInterval         time.Duration
	CircuitCooldown         time.Duration
	HalfOpenMaxRequests     int
	HalfOpenSuccesses       int
	ActiveFailureThreshold  int
	ActiveSuccessThreshold  int
	SlowLatencyThreshold    time.Duration
	Now                     func() time.Time
	OnCircuitTransition     func(circuitTransition)
	OnHealthTransition      func(healthTransition)
}

func defaultResilienceConfig() resilienceConfig {
	return resilienceConfig{
		Selector:                selectorRoundRobin,
		PassiveFailureThreshold: defaultPassiveFailureThreshold,
		PassiveInterval:         defaultPassiveInterval,
		CircuitCooldown:         defaultCircuitCooldown,
		HalfOpenMaxRequests:     defaultHalfOpenMaxRequests,
		HalfOpenSuccesses:       defaultHalfOpenSuccesses,
		ActiveFailureThreshold:  defaultActiveFailureThreshold,
		ActiveSuccessThreshold:  defaultActiveSuccessThreshold,
		Now:                     time.Now,
	}
}

func (c *resilienceConfig) setDefaults() {
	if c.Selector == "" {
		c.Selector = selectorRoundRobin
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

func (c resilienceConfig) validate() error {
	switch c.Selector {
	case selectorRoundRobin, selectorLeastInFlight:
	default:
		return fmt.Errorf("selector must be %q or %q", selectorRoundRobin, selectorLeastInFlight)
	}
	if c.PassiveFailureThreshold <= 0 || c.PassiveInterval <= 0 || c.CircuitCooldown <= 0 {
		return fmt.Errorf("passive threshold, interval, and circuit cooldown must be greater than zero")
	}
	if c.HalfOpenMaxRequests <= 0 || c.HalfOpenSuccesses <= 0 || c.HalfOpenSuccesses > c.HalfOpenMaxRequests {
		return fmt.Errorf("half-open successes must be positive and no greater than the half-open request limit")
	}
	if c.ActiveFailureThreshold <= 0 || c.ActiveSuccessThreshold <= 0 {
		return fmt.Errorf("active health thresholds must be greater than zero")
	}
	if c.SlowLatencyThreshold < 0 {
		return fmt.Errorf("slow latency threshold must not be negative")
	}
	if c.Now == nil {
		return fmt.Errorf("clock is required")
	}
	return nil
}

type backendConfig struct {
	Alias              string
	Address            string
	Authority          string
	MaxInFlight        int
	MaxIdleConnections int
	IdleTimeout        time.Duration
	HealthPath         string
}

func (c *backendConfig) setDefaults() {
	if c.Authority == "" {
		c.Authority = c.Address
	}
	if c.MaxIdleConnections == 0 {
		c.MaxIdleConnections = defaultBackendIdleConnections
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultBackendIdleTimeout
	}
	if c.HealthPath == "" {
		c.HealthPath = "/health"
	}
}

type backendSnapshot struct {
	Alias             string       `json:"alias"`
	Healthy           bool         `json:"healthy"`
	Circuit           circuitState `json:"circuit"`
	InFlight          int64        `json:"in_flight"`
	ActiveFailures    int          `json:"active_failures"`
	ActiveSuccesses   int          `json:"active_successes"`
	PassiveFailures   int          `json:"passive_failures"`
	HalfOpenInFlight  int          `json:"half_open_in_flight"`
	HalfOpenSuccesses int          `json:"half_open_successes"`
	OpenedAt          time.Time    `json:"opened_at,omitempty"`
}

type proxyBackend struct {
	config    backendConfig
	admission chan struct{}
	inFlight  atomic.Int64
	policy    resilienceConfig

	stateMu            sync.Mutex
	healthy            bool
	activeFailures     int
	activeSuccesses    int
	circuit            circuitState
	passiveFailures    int
	passiveWindowStart time.Time
	openedAt           time.Time
	halfOpenInFlight   int
	halfOpenSuccesses  int

	idleMu     sync.Mutex
	idle       []idleUpstreamConnection
	poolClosed bool
}

type passiveOutcome uint8

const (
	passiveNeutral passiveOutcome = iota
	passiveSuccess
	passiveFailure
)

type backendReservation struct {
	backend  *proxyBackend
	halfOpen bool
	released atomic.Bool
}

func (r *backendReservation) Complete(outcome passiveOutcome, at time.Time) {
	if r == nil || r.backend == nil || !r.released.CompareAndSwap(false, true) {
		return
	}
	transition := r.backend.recordPassive(outcome, r.halfOpen, at)
	r.backend.inFlight.Add(-1)
	<-r.backend.admission
	r.backend.publishTransition(transition)
}

func (r *backendReservation) Release() {
	if r == nil {
		return
	}
	now := time.Now()
	if r.backend != nil && r.backend.policy.Now != nil {
		now = r.backend.policy.Now()
	}
	r.Complete(passiveNeutral, now)
}

type backendPool struct {
	backends   []*proxyBackend
	sequence   atomic.Uint64
	resilience resilienceConfig
}

func newBackendPool(configs []backendConfig) (*backendPool, error) {
	return newBackendPoolWithConfig(configs, defaultResilienceConfig())
}

func newBackendPoolWithConfig(configs []backendConfig, resilience resilienceConfig) (*backendPool, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("backend pool requires at least one backend")
	}
	resilience.setDefaults()
	if err := resilience.validate(); err != nil {
		return nil, err
	}
	pool := &backendPool{backends: make([]*proxyBackend, 0, len(configs)), resilience: resilience}
	aliases := make(map[string]struct{}, len(configs))
	for _, config := range configs {
		config.setDefaults()
		if err := config.validate(); err != nil {
			return nil, err
		}
		aliasKey := strings.ToLower(config.Alias)
		if _, exists := aliases[aliasKey]; exists {
			return nil, fmt.Errorf("backend alias %q is duplicated", config.Alias)
		}
		aliases[aliasKey] = struct{}{}
		pool.backends = append(pool.backends, &proxyBackend{
			config:    config,
			admission: make(chan struct{}, config.MaxInFlight),
			policy:    resilience,
			healthy:   true,
			circuit:   circuitClosed,
			idle:      make([]idleUpstreamConnection, 0, config.MaxIdleConnections),
		})
	}
	return pool, nil
}

func (p *backendPool) reserveNext() (*backendReservation, error) {
	return p.reserveNextExcluding(nil)
}

func (p *backendPool) reserveNextExcluding(excluded map[string]struct{}) (*backendReservation, error) {
	if p == nil || len(p.backends) == 0 {
		return nil, &proxyError{Kind: proxyNoBackend}
	}
	now := p.resilience.Now()
	order := p.candidateOrder()
	eligible := false
	for _, index := range order {
		backend := p.backends[index]
		if _, skip := excluded[strings.ToLower(backend.config.Alias)]; skip {
			continue
		}
		reservation, stateEligible := backend.tryReserve(now)
		eligible = eligible || stateEligible
		if reservation != nil {
			return reservation, nil
		}
	}
	if eligible {
		return nil, &proxyError{Kind: proxyAdmissionRejected}
	}
	return nil, &proxyError{Kind: proxyNoBackend}
}

func (p *backendPool) candidateOrder() []int {
	count := len(p.backends)
	order := make([]int, count)
	start := int((p.sequence.Add(1) - 1) % uint64(count))
	for offset := range count {
		order[offset] = (start + offset) % count
	}
	if p.resilience.Selector != selectorLeastInFlight {
		return order
	}
	for i := 1; i < len(order); i++ {
		value := order[i]
		valueLoad := p.backends[value].inFlight.Load()
		j := i - 1
		for j >= 0 && p.backends[order[j]].inFlight.Load() > valueLoad {
			order[j+1] = order[j]
			j--
		}
		order[j+1] = value
	}
	return order
}

func (p *backendPool) Close() error {
	if p == nil {
		return nil
	}
	var first error
	for _, backend := range p.backends {
		if err := backend.closeIdleConnections(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (p *backendPool) snapshots() []backendSnapshot {
	if p == nil {
		return nil
	}
	snapshots := make([]backendSnapshot, 0, len(p.backends))
	for _, backend := range p.backends {
		snapshots = append(snapshots, backend.snapshot())
	}
	return snapshots
}

func (b *proxyBackend) tryReserve(now time.Time) (*backendReservation, bool) {
	transition := b.advanceCooldown(now)
	b.publishTransition(transition)

	b.stateMu.Lock()
	if !b.healthy || b.circuit == circuitOpen || (b.circuit == circuitHalfOpen && b.halfOpenInFlight >= b.policy.HalfOpenMaxRequests) {
		b.stateMu.Unlock()
		return nil, false
	}
	halfOpen := b.circuit == circuitHalfOpen
	if halfOpen {
		b.halfOpenInFlight++
	}
	b.stateMu.Unlock()

	select {
	case b.admission <- struct{}{}:
		b.inFlight.Add(1)
		return &backendReservation{backend: b, halfOpen: halfOpen}, true
	default:
		if halfOpen {
			b.stateMu.Lock()
			b.halfOpenInFlight--
			b.stateMu.Unlock()
		}
		return nil, true
	}
}

func (b *proxyBackend) advanceCooldown(now time.Time) *circuitTransition {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if b.circuit != circuitOpen || now.Sub(b.openedAt) < b.policy.CircuitCooldown {
		return nil
	}
	return b.transitionLocked(circuitHalfOpen, now, "cooldown_elapsed")
}

func (b *proxyBackend) recordPassive(outcome passiveOutcome, halfOpen bool, now time.Time) *circuitTransition {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if halfOpen && b.halfOpenInFlight > 0 {
		b.halfOpenInFlight--
	}
	if outcome == passiveNeutral {
		return nil
	}
	if b.circuit == circuitHalfOpen && halfOpen {
		if outcome == passiveFailure {
			return b.transitionLocked(circuitOpen, now, "half_open_failure")
		}
		b.halfOpenSuccesses++
		if b.halfOpenSuccesses >= b.policy.HalfOpenSuccesses {
			return b.transitionLocked(circuitClosed, now, "half_open_recovered")
		}
		return nil
	}
	if b.circuit != circuitClosed {
		return nil
	}
	if b.passiveWindowStart.IsZero() || now.Sub(b.passiveWindowStart) >= b.policy.PassiveInterval {
		b.passiveWindowStart = now
		b.passiveFailures = 0
	}
	if outcome == passiveSuccess {
		b.passiveFailures = 0
		b.passiveWindowStart = now
		return nil
	}
	b.passiveFailures++
	if b.passiveFailures >= b.policy.PassiveFailureThreshold {
		return b.transitionLocked(circuitOpen, now, "passive_failure_threshold")
	}
	return nil
}

func (b *proxyBackend) recordActive(success bool) {
	b.stateMu.Lock()
	previous := b.healthy
	reason := "active_probe_failure"
	if success {
		b.activeFailures = 0
		b.activeSuccesses++
		if b.activeSuccesses >= b.policy.ActiveSuccessThreshold {
			b.healthy = true
		}
		reason = "active_probe_recovered"
	} else {
		b.activeSuccesses = 0
		b.activeFailures++
		if b.activeFailures >= b.policy.ActiveFailureThreshold {
			b.healthy = false
		}
	}
	current := b.healthy
	b.stateMu.Unlock()
	if previous != current && b.policy.OnHealthTransition != nil {
		b.policy.OnHealthTransition(healthTransition{
			BackendAlias: b.config.Alias,
			FromHealthy:  previous,
			ToHealthy:    current,
			At:           b.policy.Now(),
			Reason:       reason,
		})
	}
}

func (b *proxyBackend) beginCircuitProbe(now time.Time) (bool, *circuitTransition) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	var transition *circuitTransition
	if b.circuit == circuitOpen && now.Sub(b.openedAt) >= b.policy.CircuitCooldown {
		transition = b.transitionLocked(circuitHalfOpen, now, "cooldown_elapsed")
	}
	if b.circuit != circuitHalfOpen || b.halfOpenInFlight >= b.policy.HalfOpenMaxRequests {
		return false, transition
	}
	b.halfOpenInFlight++
	return true, transition
}

func (b *proxyBackend) completeCircuitProbe(success bool, permitted bool, now time.Time) {
	if !permitted {
		return
	}
	outcome := passiveFailure
	if success {
		outcome = passiveSuccess
	}
	transition := b.recordPassive(outcome, true, now)
	b.publishTransition(transition)
}

func (b *proxyBackend) transitionLocked(to circuitState, at time.Time, reason string) *circuitTransition {
	from := b.circuit
	if from == to && to != circuitOpen {
		return nil
	}
	b.circuit = to
	b.passiveFailures = 0
	b.passiveWindowStart = at
	b.halfOpenInFlight = 0
	b.halfOpenSuccesses = 0
	if to == circuitOpen {
		b.openedAt = at
	} else if to == circuitClosed {
		b.openedAt = time.Time{}
	}
	return &circuitTransition{BackendAlias: b.config.Alias, From: from, To: to, At: at, Reason: reason}
}

func (b *proxyBackend) publishTransition(transition *circuitTransition) {
	if transition != nil && b.policy.OnCircuitTransition != nil {
		b.policy.OnCircuitTransition(*transition)
	}
}

func (b *proxyBackend) snapshot() backendSnapshot {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return backendSnapshot{
		Alias:             b.config.Alias,
		Healthy:           b.healthy,
		Circuit:           b.circuit,
		InFlight:          b.inFlight.Load(),
		ActiveFailures:    b.activeFailures,
		ActiveSuccesses:   b.activeSuccesses,
		PassiveFailures:   b.passiveFailures,
		HalfOpenInFlight:  b.halfOpenInFlight,
		HalfOpenSuccesses: b.halfOpenSuccesses,
		OpenedAt:          b.openedAt,
	}
}
