package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScenarioRejectsOverflowingScheduleWithoutPanic(t *testing.T) {
	value := defaultDemoScenario()
	value.Steps[0].AtMS = maxInt()
	value.Steps[0].JitterMS = 1
	if err := value.validate(); err == nil {
		t.Fatal("overflowing schedule was accepted")
	}
}

func TestScenarioInputHasHardByteLimit(t *testing.T) {
	_, err := parseScenario(bytes.Repeat([]byte{' '}, 1<<20+1))
	if err == nil || !strings.Contains(err.Error(), "scenario exceeds") {
		t.Fatalf("oversized scenario error = %v", err)
	}
}

func TestAllocationScaleConfigurationIsRejected(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = maxInt()
	if err := config.Validate(); err == nil {
		t.Fatal("allocation-scale downstream connection limit was accepted")
	}

	backend := backendConfig{Alias: "oversized", Address: "127.0.0.1:8080", MaxInFlight: maxInt(), MaxIdleConnections: 1, IdleTimeout: time.Second, HealthPath: "/health"}
	if err := backend.validate(); err == nil {
		t.Fatal("allocation-scale backend admission limit was accepted")
	}
	observerConfig := defaultObservabilityConfig()
	observerConfig.LedgerCapacity = maxInt()
	if _, err := newObservability(observerConfig, nil); err == nil {
		t.Fatal("allocation-scale ledger capacity was accepted")
	}
	if _, err := newSSEHub(maxSSESubscribers, maxSubscriberQueue); err == nil {
		t.Fatal("allocation-scale aggregate subscriber queues were accepted")
	}
}

func TestHTTPLimitsRejectAllocationScaleConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*httpLimits)
	}{
		{name: "start line", mutate: func(l *httpLimits) { l.MaxStartLineBytes = maxInt() }},
		{name: "headers", mutate: func(l *httpLimits) { l.MaxHeaderBytes = maxInt() }},
		{name: "header fields", mutate: func(l *httpLimits) { l.MaxHeaderFields = maxInt() }},
		{name: "chunk line", mutate: func(l *httpLimits) { l.MaxChunkLineBytes = maxInt() }},
		{name: "chunk count", mutate: func(l *httpLimits) { l.MaxChunkCount = maxInt() }},
		{name: "trailers", mutate: func(l *httpLimits) { l.MaxTrailerBytes = maxInt() }},
		{name: "trailer fields", mutate: func(l *httpLimits) { l.MaxTrailerFields = maxInt() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := defaultHTTPLimits()
			test.mutate(&limits)
			if err := limits.validate(); err == nil {
				t.Fatal("allocation-scale HTTP limit was accepted")
			}
		})
	}
}

func TestBackendFlagCountIsBoundedDuringParsing(t *testing.T) {
	var values backendFlagValues
	for index := 0; index < maxBackendPoolSize; index++ {
		if err := values.Set(fmt.Sprintf("node-%d=127.0.0.1:8080", index)); err != nil {
			t.Fatalf("bounded backend %d: %v", index, err)
		}
	}
	if err := values.Set("overflow=127.0.0.1:8080"); err == nil {
		t.Fatal("backend flag parser accepted an unbounded backend")
	}
}

func TestCLIRejectsDurationOverflowBeforeConversion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	tooLarge := fmt.Sprint(maxInt())
	if code := runDemo([]string{"--duration-ms", tooLarge}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("demo exit = %d, stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runBenchCommand([]string{"--target", "127.0.0.1:1", "--duration-ms", tooLarge}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("bench exit = %d, stderr=%q", code, stderr.String())
	}
}

func TestProtocolIdentityAndPathFieldsHaveByteCaps(t *testing.T) {
	config := DefaultConfig()
	config.Listen = strings.Repeat("a", maxBackendAddressBytes+1) + ":8080"
	if err := config.Validate(); err == nil {
		t.Fatal("oversized listen address was accepted")
	}
	backend := backendConfig{Alias: "bounded", Address: "127.0.0.1:8080", Authority: strings.Repeat("a", maxBackendAddressBytes+1), MaxInFlight: 1, MaxIdleConnections: 1, IdleTimeout: time.Second, HealthPath: "/health"}
	if err := backend.validate(); err == nil {
		t.Fatal("oversized backend authority was accepted")
	}
	proxy := defaultProxyConfig()
	proxy.ViaName = strings.Repeat("a", maxBackendAliasBytes+1)
	if err := proxy.validate(); err == nil {
		t.Fatal("oversized proxy identity was accepted")
	}
	value := defaultDemoScenario()
	value.Load.Path = "/" + strings.Repeat("a", 4_096)
	if err := value.validate(); err == nil {
		t.Fatal("oversized scenario path was accepted")
	}
}

func TestHTTPResponseRejectsForbiddenNoContentFraming(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "informational content length", raw: "HTTP/1.1 100 Continue\r\nContent-Length: 1\r\n\r\n"},
		{name: "informational transfer encoding", raw: "HTTP/1.1 100 Continue\r\nTransfer-Encoding: chunked\r\n\r\n"},
		{name: "no content content length", raw: "HTTP/1.1 204 No Content\r\nContent-Length: 1\r\n\r\n"},
		{name: "no content transfer encoding", raw: "HTTP/1.1 204 No Content\r\nTransfer-Encoding: chunked\r\n\r\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readHTTPResponse(bufio.NewReader(strings.NewReader(test.raw)), defaultHTTPLimits(), "GET")
			if protocolKind(err) != protocolMalformedHeader {
				t.Fatalf("response error = %v, want malformed header", err)
			}
		})
	}
}

func TestActiveHealthCheckerRejectsNilParent(t *testing.T) {
	pool, err := newBackendPool([]backendConfig{{Alias: "health", Address: "127.0.0.1:8080", MaxInFlight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	checker, err := newActiveHealthChecker(pool, defaultProxyConfig(), defaultActiveHealthConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Start(nil); err == nil {
		t.Fatal("nil health-check context was accepted")
	}
}

type blockingTestCloser struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (c *blockingTestCloser) Close() error {
	c.calls.Add(1)
	close(c.started)
	<-c.release
	return nil
}

func TestCancellationCloserJoinsStartedCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closer := &blockingTestCloser{started: make(chan struct{}), release: make(chan struct{})}
	stop := closeOnContextDone(ctx, closer)
	cancel()
	<-closer.started
	joined := make(chan struct{})
	go func() {
		stop()
		close(joined)
	}()
	select {
	case <-joined:
		t.Fatal("cancellation callback was not joined")
	default:
	}
	close(closer.release)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("cancellation callback did not terminate")
	}
	if closer.calls.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", closer.calls.Load())
	}
}

func TestHTTPSlowlorisDoesNotBlockNormalClient(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/", func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		return textResponse(200, "fast\n"), nil
	})
	config := testHTTPConfig()
	address, server, stop := startHTTPTestServer(t, router, config)
	defer stop()
	slow := dialHTTPTestServer(t, address)
	defer slow.Close()
	if _, err := io.WriteString(slow, "GET / HTTP/1.1\r\nHost: slow"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return server.Stats().Active == 1 })

	started := time.Now()
	response := rawHTTPExchange(t, address, "GET / HTTP/1.1\r\nHost: fast\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 200 || string(response.Body) != "fast\n" {
		t.Fatalf("normal response = %d %q", response.StatusCode, response.Body)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("normal client took %s while Slowloris connection was open", elapsed)
	}
}

func TestSlowUpstreamDoesNotBlockHealthyBackend(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})
	slowAddress, stopSlow := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		slowStarted <- struct{}{}
		<-releaseSlow
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nslow")
	})
	defer stopSlow()
	healthyAddress, stopHealthy := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nfast")
	})
	defer stopHealthy()
	pool, err := newBackendPool([]backendConfig{
		{Alias: "slow", Address: slowAddress, MaxInFlight: 1},
		{Alias: "healthy", Address: healthyAddress, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	config := defaultProxyConfig()
	config.MaxAttempts = 1
	config.ReadTimeout = time.Second
	config.RetryTimeout = time.Second
	config.NewRequestID = func() (string, error) { return "phase8-isolation-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}}
	slowDone := make(chan *httpResponse, 1)
	go func() { slowDone <- executeProxyRequest(context.Background(), request, pool, config) }()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow upstream was not selected")
	}
	started := time.Now()
	response := executeProxyRequest(context.Background(), request, pool, config)
	if response.StatusCode != 200 || string(response.Body) != "fast" {
		t.Fatalf("healthy response = %d %q", response.StatusCode, response.Body)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("healthy backend took %s while another upstream was slow", elapsed)
	}
	close(releaseSlow)
	select {
	case response := <-slowDone:
		if response.StatusCode != 200 || string(response.Body) != "slow" {
			t.Fatalf("slow response = %d %q", response.StatusCode, response.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("slow request did not terminate")
	}
}

func TestFailureMappingStatusProxyStatusLedgerAndMetricAgree(t *testing.T) {
	observer, err := newObservability(defaultObservabilityConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	failure := &proxyError{Kind: proxyUpstreamIncomplete, BackendAlias: "safe-alias", Err: fmt.Errorf("private 10.0.0.9:9000")}
	recordProxyAttemptFailure(observer, "mapping-id", "route", failure.BackendAlias, 1, failure, time.Millisecond)
	response := proxyFailureResponse(failure, "mapping-id", &responseCommitState{})
	event := observer.ledger.snapshotSince(0).Events[0]
	metrics := observer.metrics.failureSnapshot()
	if response.StatusCode != 502 || responseHeader(response, "Proxy-Status") != "anvil; error=http_response_incomplete; next-hop=safe-alias" || event.Reason != string(proxyUpstreamIncomplete) || metrics.UpstreamIncomplete != 1 {
		t.Fatalf("mapping mismatch: response=%+v event=%+v metrics=%+v", response, event, metrics)
	}
	encoded := fmt.Sprintf("%+v %+v", response, event)
	if strings.Contains(encoded, "10.0.0.9") {
		t.Fatalf("mapping output leaked private address: %s", encoded)
	}
}
