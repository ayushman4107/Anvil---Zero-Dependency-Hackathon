package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct{ nanos atomic.Int64 }

func newTestClock(at time.Time) *testClock {
	clock := &testClock{}
	clock.nanos.Store(at.UnixNano())
	return clock
}

func (c *testClock) Now() time.Time                 { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) Advance(duration time.Duration) { c.nanos.Add(int64(duration)) }

func TestRoundRobinSelectorRotatesThreeEligibleBackends(t *testing.T) {
	pool, err := newBackendPool([]backendConfig{
		{Alias: "alpha", Address: "127.0.0.1:8001", MaxInFlight: 2},
		{Alias: "beta", Address: "127.0.0.1:8002", MaxInFlight: 2},
		{Alias: "gamma", Address: "127.0.0.1:8003", MaxInFlight: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for range 6 {
		reservation, reserveErr := pool.reserveNext()
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		got = append(got, reservation.backend.config.Alias)
		reservation.Release()
	}
	if joined := strings.Join(got, ","); joined != "alpha,beta,gamma,alpha,beta,gamma" {
		t.Fatalf("round robin = %s", joined)
	}
}

func TestLeastInFlightSelectorDeprioritizesBusyBackend(t *testing.T) {
	policy := defaultResilienceConfig()
	policy.Selector = selectorLeastInFlight
	pool, err := newBackendPoolWithConfig([]backendConfig{
		{Alias: "alpha", Address: "127.0.0.1:8001", MaxInFlight: 4},
		{Alias: "beta", Address: "127.0.0.1:8002", MaxInFlight: 4},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	first, err := pool.reserveNext()
	if err != nil || first.backend.config.Alias != "alpha" {
		t.Fatalf("first = %v, %v", first, err)
	}
	second, err := pool.reserveNext()
	if err != nil || second.backend.config.Alias != "beta" {
		t.Fatalf("least-in-flight second = %v, %v", second, err)
	}
	first.Release()
	second.Release()
}

func TestCircuitStateMachineUsesVirtualTimeAndBoundedHalfOpen(t *testing.T) {
	clock := newTestClock(time.Unix(100, 0))
	policy := defaultResilienceConfig()
	policy.Now = clock.Now
	policy.PassiveFailureThreshold = 2
	policy.CircuitCooldown = 10 * time.Second
	policy.HalfOpenMaxRequests = 1
	policy.HalfOpenSuccesses = 1
	var transitionsMu sync.Mutex
	var transitions []circuitTransition
	var backend *proxyBackend
	policy.OnCircuitTransition = func(transition circuitTransition) {
		_ = backend.snapshot() // Re-entrant read proves callbacks run outside the state lock.
		transitionsMu.Lock()
		transitions = append(transitions, transition)
		transitionsMu.Unlock()
	}
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: "127.0.0.1:8001", MaxInFlight: 4}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	backend = pool.backends[0]
	for range 2 {
		reservation, reserveErr := pool.reserveNext()
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		reservation.Complete(passiveFailure, clock.Now())
	}
	if snapshot := backend.snapshot(); snapshot.Circuit != circuitOpen {
		t.Fatalf("circuit = %s, want open", snapshot.Circuit)
	}
	if _, err := pool.reserveNext(); proxyFailureKind(err) != proxyNoBackend {
		t.Fatalf("open selection error = %v", err)
	}
	clock.Advance(10 * time.Second)
	probe, err := pool.reserveNext()
	if err != nil || !probe.halfOpen {
		t.Fatalf("half-open reservation = %v, %v", probe, err)
	}
	if _, err := pool.reserveNext(); proxyFailureKind(err) != proxyNoBackend {
		t.Fatalf("second half-open probe error = %v", err)
	}
	probe.Complete(passiveSuccess, clock.Now())
	if snapshot := backend.snapshot(); snapshot.Circuit != circuitClosed {
		t.Fatalf("recovered circuit = %s", snapshot.Circuit)
	}
	transitionsMu.Lock()
	got := append([]circuitTransition(nil), transitions...)
	transitionsMu.Unlock()
	if len(got) != 3 || got[0].To != circuitOpen || got[1].To != circuitHalfOpen || got[2].To != circuitClosed {
		t.Fatalf("transitions = %+v", got)
	}
}

func TestHalfOpenFailureRestartsCooldown(t *testing.T) {
	clock := newTestClock(time.Unix(200, 0))
	policy := defaultResilienceConfig()
	policy.Now = clock.Now
	policy.PassiveFailureThreshold = 1
	policy.CircuitCooldown = 5 * time.Second
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: "127.0.0.1:8001", MaxInFlight: 2}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _ := pool.reserveNext()
	reservation.Complete(passiveFailure, clock.Now())
	clock.Advance(5 * time.Second)
	probe, err := pool.reserveNext()
	if err != nil {
		t.Fatal(err)
	}
	probe.Complete(passiveFailure, clock.Now())
	openedAt := pool.backends[0].snapshot().OpenedAt
	clock.Advance(4 * time.Second)
	if _, err := pool.reserveNext(); proxyFailureKind(err) != proxyNoBackend {
		t.Fatalf("reopened circuit admitted early: %v", err)
	}
	if !openedAt.Equal(time.Unix(205, 0)) {
		t.Fatalf("reopened at %v", openedAt)
	}
}

func TestPassiveFailureWindowResets(t *testing.T) {
	clock := newTestClock(time.Unix(250, 0))
	policy := defaultResilienceConfig()
	policy.Now = clock.Now
	policy.PassiveFailureThreshold = 2
	policy.PassiveInterval = 5 * time.Second
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: "127.0.0.1:8001", MaxInFlight: 2}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := pool.reserveNext()
	first.Complete(passiveFailure, clock.Now())
	clock.Advance(5 * time.Second)
	second, _ := pool.reserveNext()
	second.Complete(passiveFailure, clock.Now())
	if snapshot := pool.backends[0].snapshot(); snapshot.Circuit != circuitClosed || snapshot.PassiveFailures != 1 {
		t.Fatalf("reset window snapshot = %+v", snapshot)
	}
}

func TestActiveHealthFailureAndRecoveryThresholds(t *testing.T) {
	fixture, stopFixture := startProxyFixture(t, "health", func(*httpRequest) *httpResponse {
		return textResponse(204, "")
	})
	defer stopFixture()
	policy := defaultResilienceConfig()
	policy.ActiveFailureThreshold = 2
	policy.ActiveSuccessThreshold = 2
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: fixture, MaxInFlight: 2, HealthPath: "/health"}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	config := defaultProxyConfig()
	config.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if fail.Load() {
			return nil, errors.New("probe refused")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	checker, err := newActiveHealthChecker(pool, config, activeHealthConfig{Interval: time.Second, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	checker.runProbe(context.Background(), pool.backends[0])
	if !pool.backends[0].snapshot().Healthy {
		t.Fatal("one active failure marked backend unhealthy")
	}
	checker.runProbe(context.Background(), pool.backends[0])
	if pool.backends[0].snapshot().Healthy {
		t.Fatal("failure threshold did not mark backend unhealthy")
	}
	if _, reserveErr := pool.reserveNext(); proxyFailureKind(reserveErr) != proxyNoBackend {
		t.Fatalf("unhealthy backend selection = %v", reserveErr)
	}
	fail.Store(false)
	checker.runProbe(context.Background(), pool.backends[0])
	if pool.backends[0].snapshot().Healthy {
		t.Fatal("one recovery success marked backend healthy")
	}
	checker.runProbe(context.Background(), pool.backends[0])
	if !pool.backends[0].snapshot().Healthy {
		t.Fatal("recovery threshold did not restore backend")
	}
}

func TestActiveHealthCheckerCancellationJoinsWorkers(t *testing.T) {
	pool, err := newBackendPool([]backendConfig{{Alias: "node-a", Address: unusedTCPAddress(t), MaxInFlight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	config := defaultProxyConfig()
	config.DialTimeout = 20 * time.Millisecond
	checker, err := newActiveHealthChecker(pool, config, activeHealthConfig{Interval: 10 * time.Millisecond, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	checker.Stop()
	checker.Stop()
}

func TestActiveHealthProbeRecoversHalfOpenCircuit(t *testing.T) {
	fixture, stopFixture := startProxyFixture(t, "health", func(*httpRequest) *httpResponse {
		return textResponse(200, "ok")
	})
	defer stopFixture()
	clock := newTestClock(time.Unix(300, 0))
	policy := defaultResilienceConfig()
	policy.Now = clock.Now
	policy.PassiveFailureThreshold = 1
	policy.CircuitCooldown = 5 * time.Second
	policy.ActiveSuccessThreshold = 1
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: fixture, MaxInFlight: 2}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _ := pool.reserveNext()
	reservation.Complete(passiveFailure, clock.Now())
	clock.Advance(5 * time.Second)
	config := defaultProxyConfig()
	config.Now = clock.Now
	checker, err := newActiveHealthChecker(pool, config, activeHealthConfig{Interval: time.Second, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !checker.runProbe(context.Background(), pool.backends[0]) {
		t.Fatal("half-open active probe failed")
	}
	if snapshot := pool.backends[0].snapshot(); snapshot.Circuit != circuitClosed || !snapshot.Healthy {
		t.Fatalf("recovered snapshot = %+v", snapshot)
	}
}

func TestPOSTIsNotRetried(t *testing.T) {
	refused := unusedTCPAddress(t)
	var healthyDials atomic.Int64
	healthy, stopHealthy := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		healthyDials.Add(1)
		_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
	})
	defer stopHealthy()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "post-no-retry-id", nil }
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{
		{Alias: "refused", Address: refused, MaxInFlight: 2},
		{Alias: "healthy", Address: healthy, MaxInFlight: 2},
	}, config, testHTTPConfig())
	response := rawHTTPExchange(t, proxyAddress, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nConnection: close\r\n\r\nx", "POST")
	stopProxy()
	if response.StatusCode != 502 || healthyDials.Load() != 0 {
		t.Fatalf("POST result = %d, healthy dials = %d", response.StatusCode, healthyDials.Load())
	}
}

func TestRetryGuardRejectsCommittedResponse(t *testing.T) {
	state := &responseCommitState{}
	state.MarkCommitted()
	request := &httpRequest{Method: "GET"}
	now := time.Unix(1, 0)
	if canRetryProxyRequest(request, 1, 2, state, now, now.Add(time.Second)) {
		t.Fatal("committed downstream response was retryable")
	}
	if canRetryProxyRequest(&httpRequest{Method: "POST"}, 1, 2, &responseCommitState{}, now, now.Add(time.Second)) {
		t.Fatal("POST was retryable")
	}
	if canRetryProxyRequest(request, 1, 2, &responseCommitState{}, now.Add(time.Second), now.Add(time.Second)) {
		t.Fatal("expired retry window was retryable")
	}
}

func TestConfiguredApplicationStatusRetryIsExplicit(t *testing.T) {
	var firstCalls atomic.Int64
	first, stopFirst := startProxyFixture(t, "first", func(*httpRequest) *httpResponse {
		firstCalls.Add(1)
		return textResponse(503, "retry-me")
	})
	defer stopFirst()
	var secondCalls atomic.Int64
	second, stopSecond := startProxyFixture(t, "second", func(*httpRequest) *httpResponse {
		secondCalls.Add(1)
		return textResponse(200, "recovered")
	})
	defer stopSecond()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "phase5-status-retry-id", nil }
	config.RetryStatuses = map[int]struct{}{503: {}}
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{
		{Alias: "first", Address: first, MaxInFlight: 2},
		{Alias: "second", Address: second, MaxInFlight: 2},
	}, config, testHTTPConfig())
	response := rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	if response.StatusCode != 200 || string(response.Body) != "recovered" || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("status retry = %d %q calls %d/%d", response.StatusCode, response.Body, firstCalls.Load(), secondCalls.Load())
	}
}

func TestProxyStatusUsesRFCErrorAndBackendAlias(t *testing.T) {
	failure := &proxyError{Kind: proxyUpstreamIncomplete, BackendAlias: "orders-a", Err: fmt.Errorf("private 10.0.0.4:9000")}
	response := proxyFailureResponse(failure, "proxy-status-id", &responseCommitState{})
	value := responseHeader(response, "Proxy-Status")
	if value != "anvil; error=http_response_incomplete; next-hop=orders-a" {
		t.Fatalf("Proxy-Status = %q", value)
	}
	if strings.Contains(value, "10.0.0.4") {
		t.Fatalf("Proxy-Status leaked private address: %q", value)
	}
}

func TestProxyStatusNoEligibleBackendOmitsNextHop(t *testing.T) {
	response := proxyFailureResponse(&proxyError{Kind: proxyNoBackend}, "phase5-no-backend-id", &responseCommitState{})
	if value := responseHeader(response, "Proxy-Status"); value != "anvil; error=destination_unavailable" {
		t.Fatalf("Proxy-Status = %q", value)
	}
}

func TestUpstreamKeepAliveConnectionIsReused(t *testing.T) {
	address, accepts, stop := startPersistentUpstream(t, true)
	defer stop()
	pool, err := newBackendPool([]backendConfig{{Alias: "reused", Address: address, MaxInFlight: 2, MaxIdleConnections: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "phase5-pool-reuse-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}, BodyMode: bodyModeNone}
	for range 2 {
		response := executeProxyRequest(context.Background(), request, pool, config)
		if response.StatusCode != 200 || string(response.Body) != "ok" {
			t.Fatalf("response = %d %q", response.StatusCode, response.Body)
		}
	}
	if got := accepts.Load(); got != 1 {
		t.Fatalf("upstream accepts = %d, want one reused connection", got)
	}
}

func TestUpstreamConnectionCloseIsDiscarded(t *testing.T) {
	address, accepts, stop := startPersistentUpstream(t, false)
	defer stop()
	pool, err := newBackendPool([]backendConfig{{Alias: "closing", Address: address, MaxInFlight: 2, MaxIdleConnections: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "phase5-pool-discard-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}, BodyMode: bodyModeNone}
	for range 2 {
		if response := executeProxyRequest(context.Background(), request, pool, config); response.StatusCode != 200 {
			t.Fatalf("response status = %d", response.StatusCode)
		}
	}
	if got := accepts.Load(); got != 2 {
		t.Fatalf("upstream accepts = %d, want two discarded connections", got)
	}
}

func TestConcurrentBackendStateAndReservations(t *testing.T) {
	policy := defaultResilienceConfig()
	policy.PassiveFailureThreshold = 10_000
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: "127.0.0.1:8001", MaxInFlight: 64}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	backend := pool.backends[0]
	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := 0; iteration < 200; iteration++ {
				backend.recordActive((worker+iteration)%3 != 0)
				reservation, reserveErr := pool.reserveNext()
				if reserveErr == nil {
					reservation.Complete(passiveSuccess, time.Now())
				}
				_ = backend.snapshot()
			}
		}(worker)
	}
	workers.Wait()
	if backend.inFlight.Load() != 0 || len(backend.admission) != 0 {
		t.Fatalf("reservation leak: in-flight=%d admission=%d", backend.inFlight.Load(), len(backend.admission))
	}
}

func startPersistentUpstream(t *testing.T, keepAlive bool) (string, *atomic.Int64, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepts atomic.Int64
	var connections sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepts.Add(1)
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer connection.Close()
				reader := bufio.NewReader(connection)
				writer := bufio.NewWriter(connection)
				for {
					if _, readErr := readHTTPRequest(reader, defaultHTTPLimits()); readErr != nil {
						return
					}
					response := textResponse(200, "ok")
					response.Close = !keepAlive
					if writeErr := writeHTTPResponse(writer, response, "GET"); writeErr != nil {
						return
					}
					if flushErr := writer.Flush(); flushErr != nil {
						return
					}
					if !keepAlive {
						return
					}
				}
			}()
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = listener.Close()
			<-acceptDone
			connections.Wait()
		})
	}
	return listener.Addr().String(), &accepts, stop
}
