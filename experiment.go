package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

type experimentLab struct {
	scenario     scenario
	hash         string
	fixtures     []*fixtureBackend
	fixtureByKey map[string]*fixtureBackend
	pool         *backendPool
	observer     *observability
	proxyServer  *tcpServer
	adminServer  *tcpServer
	health       *activeHealthChecker
	proxyAddress string
	adminAddress string
}

type labServiceResult struct {
	name string
	err  error
}

func newExperimentLab(value scenario) (*experimentLab, error) {
	if err := value.validate(); err != nil {
		return nil, err
	}
	hash, err := value.canonicalHash()
	if err != nil {
		return nil, err
	}
	lab := &experimentLab{scenario: value, hash: hash, fixtureByKey: make(map[string]*fixtureBackend, len(value.Fixtures))}
	cleanup := true
	defer func() {
		if cleanup {
			lab.closeUnstarted()
		}
	}()
	backendConfigs := make([]backendConfig, 0, len(value.Fixtures))
	for _, spec := range value.Fixtures {
		fixture, err := newFixtureBackend(spec)
		if err != nil {
			return nil, fmt.Errorf("fixture %q: %w", spec.Alias, err)
		}
		lab.fixtures = append(lab.fixtures, fixture)
		lab.fixtureByKey[strings.ToLower(spec.Alias)] = fixture
		backendConfigs = append(backendConfigs, backendConfig{Alias: spec.Alias, Address: fixture.address, MaxInFlight: 128, MaxIdleConnections: 8, IdleTimeout: 5 * time.Second, HealthPath: "/health"})
	}
	resilience := defaultResilienceConfig()
	resilience.Selector = selectorRoundRobin
	resilience.PassiveFailureThreshold = value.Resilience.PassiveFailures
	resilience.PassiveInterval = time.Duration(value.Resilience.PassiveIntervalMS) * time.Millisecond
	resilience.CircuitCooldown = time.Duration(value.Resilience.CircuitCooldownMS) * time.Millisecond
	resilience.HalfOpenMaxRequests = max(value.Resilience.HalfOpenSuccesses, 1)
	resilience.HalfOpenSuccesses = value.Resilience.HalfOpenSuccesses
	resilience.ActiveFailureThreshold = value.Resilience.HealthFailures
	resilience.ActiveSuccessThreshold = value.Resilience.HealthSuccesses
	var observer *observability
	resilience.OnCircuitTransition = func(transition circuitTransition) {
		if observer != nil {
			observer.recordCircuitTransition(transition)
		}
	}
	resilience.OnHealthTransition = func(transition healthTransition) {
		if observer != nil {
			observer.recordHealthTransition(transition)
		}
	}
	pool, err := newBackendPoolWithConfig(backendConfigs, resilience)
	if err != nil {
		return nil, err
	}
	lab.pool = pool
	observabilityConfig := defaultObservabilityConfig()
	observabilityConfig.LedgerCapacity = value.LedgerCapacity
	observer, err = newObservability(observabilityConfig, pool)
	if err != nil {
		return nil, err
	}
	lab.observer = observer

	proxyConfig := defaultProxyConfig()
	proxyConfig.RouteAlias = "experiment"
	proxyConfig.MaxAttempts = value.Resilience.MaxAttempts
	proxyConfig.RetryTimeout = time.Duration(value.Resilience.RetryTimeoutMS) * time.Millisecond
	proxyConfig.ReadTimeout = time.Duration(value.Load.TimeoutMS) * time.Millisecond
	proxyConfig.WriteTimeout = time.Duration(value.Load.TimeoutMS) * time.Millisecond
	proxyConfig.DialTimeout = time.Duration(value.Load.TimeoutMS) * time.Millisecond
	proxyConfig.Observability = observer
	proxyConfig.RetryStatuses = make(map[int]struct{}, 100)
	for status := 500; status <= 599; status++ {
		proxyConfig.RetryStatuses[status] = struct{}{}
	}
	baseDialer := &net.Dialer{Timeout: proxyConfig.DialTimeout}
	proxyConfig.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		for _, fixture := range lab.fixtures {
			if fixture.address == address && fixture.mode() == fixtureUnavailable {
				return nil, &net.OpError{Op: "dial", Net: network, Addr: stringAddress(address), Err: syscall.ECONNREFUSED}
			}
		}
		return baseDialer.DialContext(ctx, network, address)
	}
	proxyHandler, err := newProxyHandler(pool, proxyConfig)
	if err != nil {
		return nil, err
	}
	router := newRouteTree()
	if err := router.Register(anyMethod, "/", proxyHandler); err != nil {
		return nil, err
	}
	if err := router.Register(anyMethod, "/*path", proxyHandler); err != nil {
		return nil, err
	}
	serverConfig := DefaultConfig()
	serverConfig.Listen = "127.0.0.1:0"
	serverConfig.MaxConnections = max(value.Load.Workers*4, 32)
	serverConfig.ReadTimeoutMS = max(value.Load.TimeoutMS*2, 1_000)
	serverConfig.WriteTimeoutMS = max(value.Load.TimeoutMS*2, 1_000)
	proxyListener, err := net.Listen("tcp", serverConfig.Listen)
	if err != nil {
		return nil, err
	}
	proxyConnectionHandler, err := newHTTPConnectionHandler(router, serverConfig.httpServerConfig())
	if err != nil {
		_ = proxyListener.Close()
		return nil, err
	}
	lab.proxyServer, err = newTCPServer(proxyListener, serverConfig.tcpServerConfig(), proxyConnectionHandler)
	if err != nil {
		_ = proxyListener.Close()
		return nil, err
	}
	lab.proxyAddress = proxyListener.Addr().String()
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	adminConfig := defaultAdminConfig()
	adminConfig.Heartbeat = 500 * time.Millisecond
	lab.adminServer, err = newAdminServer(adminListener, adminConfig, observer)
	if err != nil {
		_ = adminListener.Close()
		return nil, err
	}
	lab.adminAddress = adminListener.Addr().String()
	observer.setServers(lab.proxyServer, lab.adminServer)
	healthConfig := activeHealthConfig{Interval: time.Duration(value.Resilience.HealthIntervalMS) * time.Millisecond, Timeout: time.Duration(value.Resilience.HealthTimeoutMS) * time.Millisecond}
	lab.health, err = newActiveHealthChecker(pool, proxyConfig, healthConfig)
	if err != nil {
		return nil, err
	}
	cleanup = false
	return lab, nil
}

type stringAddress string

func (a stringAddress) Network() string { return "tcp" }
func (a stringAddress) String() string  { return string(a) }

func (l *experimentLab) run(parent context.Context, progress io.Writer) (resilienceReceipt, error) {
	if parent == nil {
		return resilienceReceipt{}, fmt.Errorf("experiment context is required")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	serviceCount := len(l.fixtures) + 2
	results := make(chan labServiceResult, serviceCount)
	for _, fixture := range l.fixtures {
		fixture := fixture
		go func() { results <- labServiceResult{name: "fixture " + fixture.alias, err: fixture.server.Serve(ctx)} }()
	}
	go func() { results <- labServiceResult{name: "proxy", err: l.proxyServer.Serve(ctx)} }()
	go func() { results <- labServiceResult{name: "admin", err: l.adminServer.Serve(ctx)} }()
	if err := l.health.Start(ctx); err != nil {
		cancel()
		l.waitServices(results, serviceCount)
		return resilienceReceipt{}, err
	}
	if progress != nil {
		fmt.Fprintf(progress, "Anvil experiment %q started; proxy http://%s/ dashboard http://%s/\n", l.scenario.Name, l.proxyAddress, l.adminAddress)
	}
	started := time.Now()
	scheduleDone := make(chan struct{})
	go func() {
		defer close(scheduleDone)
		l.runSchedule(ctx, started)
	}()
	benchmark, benchmarkErr := runBenchmark(ctx, benchmarkConfig{
		Address: l.proxyAddress, Authority: l.proxyAddress, Method: "GET", Path: l.scenario.Load.Path,
		Workers: l.scenario.Load.Workers, MaxRequests: l.scenario.Load.Requests, RatePerSecond: l.scenario.Load.RatePerSecond,
		Duration: time.Duration(l.scenario.DurationMS) * time.Millisecond, Timeout: time.Duration(l.scenario.Load.TimeoutMS) * time.Millisecond,
		Limits: defaultHTTPLimits(),
	})
	<-scheduleDone
	l.waitForObserverIdle(time.Duration(l.scenario.Load.TimeoutMS*2) * time.Millisecond)
	l.health.Stop()
	ledger := l.observer.ledger.snapshotSince(0)
	fixtureSnapshots := make([]fixtureSnapshot, 0, len(l.fixtures))
	for _, fixture := range l.fixtures {
		fixtureSnapshots = append(fixtureSnapshots, fixture.snapshot())
	}
	receipt := deriveReceipt(l.scenario, l.hash, ledger, benchmark, fixtureSnapshots)
	_ = l.pool.Close()
	cancel()
	serviceErr := l.waitServices(results, serviceCount)
	if benchmarkErr != nil {
		return receipt, benchmarkErr
	}
	if serviceErr != nil {
		return receipt, serviceErr
	}
	return receipt, nil
}

func (l *experimentLab) runSchedule(ctx context.Context, started time.Time) {
	for _, scheduled := range l.scenario.schedule() {
		wait := time.Until(started.Add(scheduled.At))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
		}
		fixture := l.fixtureByKey[strings.ToLower(scheduled.Configured.Fixture)]
		profile := profileFromScenarioStep(scheduled.Configured)
		previous := fixture.mode()
		l.observer.publish(decisionEvent{
			Type: eventFixtureTransition, BackendAlias: fixture.alias,
			PreviousState: string(previous), NewState: string(profile.Mode),
			Reason: fmt.Sprintf("scenario_step_%d", scheduled.Index),
		})
		fixture.apply(profile)
	}
}

func (l *experimentLab) waitForObserverIdle(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if l.observer.metrics.activeRequests.Load() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func (l *experimentLab) waitServices(results <-chan labServiceResult, count int) error {
	var first error
	for range count {
		result := <-results
		if result.err != nil && !errors.Is(result.err, net.ErrClosed) && first == nil {
			first = fmt.Errorf("%s: %w", result.name, result.err)
		}
	}
	return first
}

func (l *experimentLab) closeUnstarted() {
	for _, fixture := range l.fixtures {
		if fixture.listener != nil {
			_ = fixture.listener.Close()
		}
	}
	if l.proxyServer != nil {
		_ = l.proxyServer.listener.Close()
	}
	if l.adminServer != nil {
		_ = l.adminServer.listener.Close()
	}
	if l.pool != nil {
		_ = l.pool.Close()
	}
	if l.observer != nil {
		l.observer.close()
	}
}

func (l *experimentLab) close() {
	if l.pool != nil {
		_ = l.pool.Close()
	}
	if l.observer != nil {
		l.observer.close()
	}
}
