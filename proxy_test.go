package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildUpstreamRequestSanitizesAndReconstructs(t *testing.T) {
	request := &httpRequest{
		Method: "POST",
		Target: "/upload?part=1",
		Headers: headerFields{
			{Name: "Host", Value: "client.example:8080"},
			{Name: "Connection", Value: "X-Hop, keep-alive"},
			{Name: "X-Hop", Value: "remove-me"},
			{Name: "Keep-Alive", Value: "timeout=5"},
			{Name: "Proxy-Connection", Value: "keep-alive"},
			{Name: "TE", Value: "trailers"},
			{Name: "Forwarded", Value: "for=spoofed"},
			{Name: "X-Forwarded-For", Value: "spoofed"},
			{Name: "X-Anvil-Request-ID", Value: "spoofed-id"},
			{Name: "Via", Value: "1.0 prior"},
			{Name: "X-End-To-End", Value: "preserve-me"},
		},
		Trailers:   headerFields{{Name: "X-Checksum", Value: "abc"}},
		Body:       []byte("payload"),
		BodyMode:   bodyModeChunked,
		RemoteAddr: "[2001:db8::1]:43100",
	}
	config := defaultProxyConfig()
	upstream := buildUpstreamRequest(request, "backend.internal:9000", "0123456789abcdef", config)

	for _, removed := range []string{"X-Hop", "Keep-Alive", "Proxy-Connection", "TE"} {
		if len(upstream.Headers.Values(removed)) != 0 {
			t.Errorf("hop-by-hop field %s survived: %v", removed, upstream.Headers.Values(removed))
		}
	}
	if host, _ := upstream.Headers.First("Host"); host != "backend.internal:9000" {
		t.Fatalf("Host = %q", host)
	}
	if connection, exists := upstream.Headers.First("Connection"); exists {
		t.Fatalf("unexpected Connection = %q", connection)
	}
	if values := upstream.Headers.Values("Via"); fmt.Sprint(values) != "[1.0 prior 1.1 anvil]" {
		t.Fatalf("Via = %v", values)
	}
	if value, _ := upstream.Headers.First("X-End-To-End"); value != "preserve-me" {
		t.Fatalf("end-to-end field = %q", value)
	}
	if value, _ := upstream.Headers.First(requestIDHeader); value != "0123456789abcdef" {
		t.Fatalf("request ID = %q", value)
	}
	if value, _ := upstream.Headers.First("Forwarded"); value != "for=\"[2001:db8::1]\";host=\"client.example:8080\";proto=http" {
		t.Fatalf("Forwarded = %q", value)
	}
	if value, _ := upstream.Headers.First("X-Forwarded-For"); value != "2001:db8::1" {
		t.Fatalf("X-Forwarded-For = %q", value)
	}
	if upstream.BodyMode != bodyModeChunked || string(upstream.Body) != "payload" {
		t.Fatalf("body = mode %s body %q", upstream.BodyMode, upstream.Body)
	}
	if value, _ := upstream.Trailers.First("X-Checksum"); value != "abc" {
		t.Fatalf("trailer = %q", value)
	}
}

func TestSanitizeUpstreamResponse(t *testing.T) {
	response := &httpResponse{
		StatusCode: 200,
		Headers: headerFields{
			{Name: "Connection", Value: "X-Upstream-Hop"},
			{Name: "X-Upstream-Hop", Value: "remove"},
			{Name: "Keep-Alive", Value: "timeout=5"},
			{Name: "Transfer-Encoding", Value: "chunked"},
			{Name: requestIDHeader, Value: "upstream-spoof"},
			{Name: "Via", Value: "1.0 upstream"},
			{Name: "X-End-To-End", Value: "preserve"},
		},
		Body:     []byte("response"),
		BodyMode: bodyModeCloseDelimited,
	}
	sanitized := sanitizeUpstreamResponse(response, "anvil")
	for _, removed := range []string{"Connection", "X-Upstream-Hop", "Keep-Alive", "Transfer-Encoding", requestIDHeader} {
		if len(sanitized.Headers.Values(removed)) != 0 {
			t.Errorf("field %s survived: %v", removed, sanitized.Headers.Values(removed))
		}
	}
	if values := sanitized.Headers.Values("Via"); fmt.Sprint(values) != "[1.0 upstream 1.1 anvil]" {
		t.Fatalf("Via = %v", values)
	}
	if value, _ := sanitized.Headers.First("X-End-To-End"); value != "preserve" {
		t.Fatalf("end-to-end field = %q", value)
	}
	if sanitized.BodyMode != bodyModeFixed {
		t.Fatalf("close-delimited body mode was not reconstructed as fixed: %s", sanitized.BodyMode)
	}
}

func TestBackendPoolRoundRobinAdmissionAndExactlyOnceRelease(t *testing.T) {
	pool, err := newBackendPool([]backendConfig{
		{Alias: "alpha", Address: "127.0.0.1:8001", MaxInFlight: 1},
		{Alias: "beta", Address: "127.0.0.1:8002", MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pool.reserveNext()
	if err != nil || first.backend.config.Alias != "alpha" {
		t.Fatalf("first reservation = %v, %v", first, err)
	}
	second, err := pool.reserveNext()
	if err != nil || second.backend.config.Alias != "beta" {
		t.Fatalf("second reservation = %v, %v", second, err)
	}
	if _, err := pool.reserveNext(); proxyFailureKind(err) != proxyAdmissionRejected {
		t.Fatalf("saturated reservation error = %v", err)
	}
	first.Release()
	first.Release()
	second.Release()
	if pool.backends[0].inFlight.Load() != 0 || pool.backends[1].inFlight.Load() != 0 {
		t.Fatalf("in-flight counts = %d, %d", pool.backends[0].inFlight.Load(), pool.backends[1].inFlight.Load())
	}
}

func TestProxyRealClientTwoFixturesRoundRobinAndMetadata(t *testing.T) {
	type observation struct {
		fixture string
		host    string
		headers headerFields
	}
	observed := make(chan observation, 2)
	fixtureA, stopA := startProxyFixture(t, "alpha", func(request *httpRequest) *httpResponse {
		host, _ := request.Headers.First("Host")
		observed <- observation{fixture: "alpha", host: host, headers: request.Headers}
		response := textResponse(200, "alpha")
		response.Headers = append(response.Headers, headerField{Name: "X-Fixture", Value: "alpha"})
		return response
	})
	defer stopA()
	fixtureB, stopB := startProxyFixture(t, "beta", func(request *httpRequest) *httpResponse {
		host, _ := request.Headers.First("Host")
		observed <- observation{fixture: "beta", host: host, headers: request.Headers}
		response := textResponse(200, "beta")
		response.Headers = append(response.Headers, headerField{Name: "X-Fixture", Value: "beta"})
		return response
	})
	defer stopB()

	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "phase4-request-id", nil }
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{
		{Alias: "alpha", Address: fixtureA, Authority: "alpha.internal", MaxInFlight: 8},
		{Alias: "beta", Address: fixtureB, Authority: "beta.internal", MaxInFlight: 8},
	}, config, testHTTPConfig())
	defer stopProxy()

	transport := &stdhttp.Transport{DisableCompression: true}
	client := &stdhttp.Client{Transport: transport, Timeout: 3 * time.Second}
	defer transport.CloseIdleConnections()
	for index, want := range []string{"alpha", "beta"} {
		request, err := stdhttp.NewRequest("GET", "http://"+proxyAddress+"/resource", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-End-To-End", "preserved")
		request.Header.Set("X-Forwarded-For", "spoofed")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("request %d: %v", index, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || string(body) != want {
			t.Fatalf("request %d = status %d body %q error %v", index, response.StatusCode, body, readErr)
		}
		if response.Header.Get(requestIDHeader) != "phase4-request-id" {
			t.Fatalf("downstream request ID = %q", response.Header.Get(requestIDHeader))
		}
		if response.Header.Get("Via") != "1.1 anvil" {
			t.Fatalf("downstream Via = %q", response.Header.Get("Via"))
		}
		if response.Header.Get("X-Fixture") != want {
			t.Fatalf("downstream end-to-end response field = %q, want %q", response.Header.Get("X-Fixture"), want)
		}
	}

	for _, want := range []struct{ fixture, authority string }{{"alpha", "alpha.internal"}, {"beta", "beta.internal"}} {
		got := <-observed
		if got.fixture != want.fixture || got.host != want.authority {
			t.Fatalf("observation = fixture %q host %q; want %q %q", got.fixture, got.host, want.fixture, want.authority)
		}
		if value, _ := got.headers.First("X-End-To-End"); value != "preserved" {
			t.Fatalf("end-to-end header = %q", value)
		}
		if value, _ := got.headers.First("X-Forwarded-For"); value != "127.0.0.1" {
			t.Fatalf("trusted immediate peer was not regenerated: %q", value)
		}
		if value, _ := got.headers.First(requestIDHeader); value != "phase4-request-id" {
			t.Fatalf("upstream request ID = %q", value)
		}
	}
}

func TestProxyCloseDelimitedResponseBecomesFixed(t *testing.T) {
	upstreamAddress, stopUpstream := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nX-End-To-End: preserved\r\n\r\nclose-body")
	})
	defer stopUpstream()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "close-delimited-id", nil }
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{{Alias: "closing", Address: upstreamAddress, MaxInFlight: 2}}, config, testHTTPConfig())
	defer stopProxy()
	response := rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 200 || response.BodyMode != bodyModeFixed || string(response.Body) != "close-body" {
		t.Fatalf("close-delimited response = status %d mode %s body %q", response.StatusCode, response.BodyMode, response.Body)
	}
	if value, _ := response.Headers.First("X-End-To-End"); value != "preserved" {
		t.Fatalf("end-to-end response field = %q", value)
	}
}

func TestProxyChunkedRequestResponseAndTrailers(t *testing.T) {
	seen := make(chan *httpRequest, 1)
	fixture, stopFixture := startProxyFixture(t, "chunked", func(request *httpRequest) *httpResponse {
		seen <- request
		return &httpResponse{
			StatusCode: 200,
			Headers:    headerFields{{Name: "X-End-To-End", Value: "yes"}},
			Trailers:   headerFields{{Name: "X-Response-Checksum", Value: "def"}},
			Body:       []byte("chunked-response"),
			BodyMode:   bodyModeChunked,
		}
	})
	defer stopFixture()
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "chunked-request-id", nil }
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{{Alias: "chunked", Address: fixture, MaxInFlight: 4}}, config, testHTTPConfig())
	defer stopProxy()

	connection := dialHTTPTestServer(t, proxyAddress)
	defer connection.Close()
	raw := "POST /stream HTTP/1.1\r\nHost: client.test\r\nTransfer-Encoding: chunked\r\nTrailer: X-Request-Checksum\r\nConnection: close\r\n\r\n5\r\nhello\r\n0\r\nX-Request-Checksum: abc\r\n\r\n"
	if _, err := io.WriteString(connection, raw); err != nil {
		t.Fatal(err)
	}
	response, err := readHTTPResponse(bufio.NewReader(connection), defaultHTTPLimits(), "POST")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.BodyMode != bodyModeChunked || string(response.Body) != "chunked-response" {
		t.Fatalf("response = status %d mode %s body %q", response.StatusCode, response.BodyMode, response.Body)
	}
	if trailer, _ := response.Trailers.First("X-Response-Checksum"); trailer != "def" {
		t.Fatalf("response trailer = %q", trailer)
	}
	request := <-seen
	if request.BodyMode != bodyModeChunked || string(request.Body) != "hello" {
		t.Fatalf("upstream request = mode %s body %q", request.BodyMode, request.Body)
	}
	if trailer, _ := request.Trailers.First("X-Request-Checksum"); trailer != "abc" {
		t.Fatalf("request trailer = %q", trailer)
	}
}

func TestProxyFailureMappingsAndSafeRetry(t *testing.T) {
	refusedAddress := unusedTCPAddress(t)
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "failure-request-id", nil }
	config.DialTimeout = 100 * time.Millisecond
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{{Alias: "refused", Address: refusedAddress, MaxInFlight: 2}}, config, testHTTPConfig())
	response := rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	if response.StatusCode != 502 || responseHeader(response, requestIDHeader) != "failure-request-id" {
		t.Fatalf("refusal response = status %d request ID %q", response.StatusCode, responseHeader(response, requestIDHeader))
	}

	timeoutAddress, stopTimeout := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		time.Sleep(200 * time.Millisecond)
	})
	config.ReadTimeout = 40 * time.Millisecond
	proxyAddress, stopProxy = startProxyTestServer(t, []backendConfig{{Alias: "slow", Address: timeoutAddress, MaxInFlight: 2}}, config, testHTTPConfig())
	response = rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	stopTimeout()
	if response.StatusCode != 504 {
		t.Fatalf("timeout status = %d, want 504", response.StatusCode)
	}

	malformedAddress, stopMalformed := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		_, _ = io.WriteString(connection, "HTTP/1.1 nope\r\n\r\n")
	})
	config.ReadTimeout = time.Second
	proxyAddress, stopProxy = startProxyTestServer(t, []backendConfig{{Alias: "malformed", Address: malformedAddress, MaxInFlight: 2}}, config, testHTTPConfig())
	response = rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	stopMalformed()
	if response.StatusCode != 502 {
		t.Fatalf("malformed status = %d, want 502", response.StatusCode)
	}

	incompleteAddress, stopIncomplete := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhi")
	})
	proxyAddress, stopProxy = startProxyTestServer(t, []backendConfig{{Alias: "incomplete", Address: incompleteAddress, MaxInFlight: 2}}, config, testHTTPConfig())
	response = rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	stopIncomplete()
	if response.StatusCode != 502 {
		t.Fatalf("incomplete status = %d, want 502", response.StatusCode)
	}

	var healthyDials atomic.Int64
	healthyAddress, stopHealthy := startRawUpstream(t, func(connection net.Conn, _ *httpRequest) {
		healthyDials.Add(1)
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	})
	proxyAddress, stopProxy = startProxyTestServer(t, []backendConfig{
		{Alias: "first-refused", Address: refusedAddress, MaxInFlight: 2},
		{Alias: "second-healthy", Address: healthyAddress, MaxInFlight: 2},
	}, config, testHTTPConfig())
	response = rawHTTPExchange(t, proxyAddress, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	stopProxy()
	stopHealthy()
	if response.StatusCode != 200 || healthyDials.Load() != 1 || string(response.Body) != "ok" {
		t.Fatalf("safe-retry result = status %d body %q healthy attempts %d", response.StatusCode, response.Body, healthyDials.Load())
	}
}

func TestProxyBodyLimitRejectsBeforeDial(t *testing.T) {
	var dials atomic.Int64
	config := defaultProxyConfig()
	config.NewRequestID = func() (string, error) { return "body-limit-id", nil }
	config.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, fmt.Errorf("must not dial")
	}
	serverConfig := testHTTPConfig()
	serverConfig.Limits.MaxBodyBytes = 4
	proxyAddress, stopProxy := startProxyTestServer(t, []backendConfig{{Alias: "unused", Address: "127.0.0.1:1", MaxInFlight: 1}}, config, serverConfig)
	defer stopProxy()
	response := rawHTTPExchange(t, proxyAddress, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 5\r\nConnection: close\r\n\r\n", "POST")
	if response.StatusCode != 413 || dials.Load() != 0 {
		t.Fatalf("body cap = status %d dials %d", response.StatusCode, dials.Load())
	}
}

func TestProxyFailureResponseMappingsDoNotLeakBackendAddress(t *testing.T) {
	tests := []struct {
		kind       proxyErrorKind
		status     int
		errorToken string
	}{
		{kind: proxyAdmissionRejected, status: 503, errorToken: "connection_limit_reached"},
		{kind: proxyCanceled, status: 503, errorToken: "proxy_internal_response"},
		{kind: proxyDialFailure, status: 502, errorToken: "connection_refused"},
		{kind: proxyWriteFailure, status: 502, errorToken: "connection_terminated"},
		{kind: proxyUpstreamProtocol, status: 502, errorToken: "http_protocol_error"},
		{kind: proxyUpstreamIncomplete, status: 502, errorToken: "http_response_incomplete"},
		{kind: proxyDialTimeout, status: 504, errorToken: "connection_timeout"},
		{kind: proxyWriteTimeout, status: 504, errorToken: "connection_write_timeout"},
		{kind: proxyUpstreamTimeout, status: 504, errorToken: "connection_read_timeout"},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			failure := &proxyError{Kind: test.kind, BackendAlias: "public-alias", Err: fmt.Errorf("127.0.0.1:65535 private detail")}
			response := proxyFailureResponse(failure, "mapping-request-id", &responseCommitState{})
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if strings.Contains(string(response.Body), "127.0.0.1") || strings.Contains(string(response.Body), "public-alias") {
				t.Fatalf("failure body leaked backend detail: %q", response.Body)
			}
			proxyStatus := responseHeader(response, "Proxy-Status")
			if proxyStatus != "anvil; error="+test.errorToken+"; next-hop=public-alias" || strings.Contains(proxyStatus, "127.0.0.1") {
				t.Fatalf("Proxy-Status = %q", proxyStatus)
			}
		})
	}
}

func TestBackendAndProxyConfigurationValidation(t *testing.T) {
	invalidBackends := []backendConfig{
		{Alias: "", Address: "127.0.0.1:80", MaxInFlight: 1},
		{Alias: "bad alias", Address: "127.0.0.1:80", MaxInFlight: 1},
		{Alias: "backend", Address: "missing-port", MaxInFlight: 1},
		{Alias: "backend", Address: "127.0.0.1:0", MaxInFlight: 1},
		{Alias: "backend", Address: "127.0.0.1:80", Authority: "bad host", MaxInFlight: 1},
		{Alias: "backend", Address: "127.0.0.1:80", MaxInFlight: 0},
	}
	for _, backend := range invalidBackends {
		if _, err := newBackendPool([]backendConfig{backend}); err == nil {
			t.Errorf("invalid backend was accepted: %+v", backend)
		}
	}
	if _, err := newBackendPool([]backendConfig{
		{Alias: "same", Address: "127.0.0.1:80", MaxInFlight: 1},
		{Alias: "SAME", Address: "127.0.0.1:81", MaxInFlight: 1},
	}); err == nil {
		t.Fatal("case-insensitive duplicate aliases were accepted")
	}

	pool, err := newBackendPool([]backendConfig{{Alias: "valid", Address: "127.0.0.1:80", MaxInFlight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	config := defaultProxyConfig()
	config.ViaName = "bad name"
	if _, err := newProxyHandler(pool, config); err == nil {
		t.Fatal("invalid Via pseudonym was accepted")
	}
	config = defaultProxyConfig()
	config.RetryStatuses = map[int]struct{}{200: {}}
	if _, err := newProxyHandler(pool, config); err == nil {
		t.Fatal("invalid retry status was accepted")
	}
	config = defaultProxyConfig()
	config.RouteAlias = "bad route alias"
	if _, err := newProxyHandler(pool, config); err == nil {
		t.Fatal("invalid observability route alias was accepted")
	}
	policy := defaultResilienceConfig()
	policy.Selector = "random"
	if _, err := newBackendPoolWithConfig([]backendConfig{{Alias: "valid", Address: "127.0.0.1:80", MaxInFlight: 1}}, policy); err == nil {
		t.Fatal("invalid selector was accepted")
	}
	policy = defaultResilienceConfig()
	policy.HalfOpenMaxRequests = 1
	policy.HalfOpenSuccesses = 2
	if _, err := newBackendPoolWithConfig([]backendConfig{{Alias: "valid", Address: "127.0.0.1:80", MaxInFlight: 1}}, policy); err == nil {
		t.Fatal("impossible half-open success threshold was accepted")
	}
}

func TestRandomRequestIDIsBoundedToken(t *testing.T) {
	first, err := randomRequestID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if !validRequestID(first) || !validRequestID(second) || first == second {
		t.Fatalf("request IDs = %q and %q", first, second)
	}
}

func TestProxyResponseCommitState(t *testing.T) {
	state := &responseCommitState{}
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/", func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		response := textResponse(200, "committed")
		response.CommitState = state
		return response, nil
	})
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()
	response := rawHTTPExchange(t, address, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 200 || !state.Committed() {
		t.Fatalf("response status = %d committed = %v", response.StatusCode, state.Committed())
	}
}

func TestBackendPoolConcurrentReservations(t *testing.T) {
	pool, err := newBackendPool([]backendConfig{{Alias: "only", Address: "127.0.0.1:8001", MaxInFlight: 16}})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 200; iteration++ {
				reservation, reserveErr := pool.reserveNext()
				if reserveErr != nil {
					continue
				}
				reservation.Release()
				reservation.Release()
			}
		}()
	}
	workers.Wait()
	if pool.backends[0].inFlight.Load() != 0 || len(pool.backends[0].admission) != 0 {
		t.Fatalf("reservation leak: in-flight %d admission %d", pool.backends[0].inFlight.Load(), len(pool.backends[0].admission))
	}
}

func startProxyFixture(t *testing.T, name string, handle func(*httpRequest) *httpResponse) (string, func()) {
	t.Helper()
	router := newRouteTree()
	handler := func(_ context.Context, request *httpRequest, _ routeParams) (*httpResponse, error) {
		return handle(request), nil
	}
	mustRegisterRoute(t, router, anyMethod, "/", handler)
	mustRegisterRoute(t, router, anyMethod, "/*path", handler)
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	_ = name
	return address, stop
}

func startProxyTestServer(t *testing.T, backends []backendConfig, proxyConfig proxyConfig, serverConfig httpServerConfig) (string, func()) {
	t.Helper()
	pool, err := newBackendPool(backends)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newProxyHandler(pool, proxyConfig)
	if err != nil {
		t.Fatal(err)
	}
	router := newRouteTree()
	mustRegisterRoute(t, router, anyMethod, "/", handler)
	mustRegisterRoute(t, router, anyMethod, "/*path", handler)
	address, _, stop := startHTTPTestServer(t, router, serverConfig)
	return address, stop
}

func startRawUpstream(t *testing.T, action func(net.Conn, *httpRequest)) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		request, readErr := readHTTPRequest(bufio.NewReader(connection), defaultHTTPLimits())
		if readErr != nil {
			return
		}
		action(connection, request)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = listener.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("raw upstream did not stop")
			}
		})
	}
	return listener.Addr().String(), stop
}

func unusedTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func responseHeader(response *httpResponse, name string) string {
	value, _ := response.Headers.First(name)
	return value
}

func proxyFailureKind(err error) proxyErrorKind {
	var failure *proxyError
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}
