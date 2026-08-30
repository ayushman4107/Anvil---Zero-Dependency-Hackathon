package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminListenRequiresExplicitLoopback(t *testing.T) {
	for _, valid := range []string{"127.0.0.1:9090", "[::1]:0"} {
		if err := validateAdminListen(valid); err != nil {
			t.Errorf("valid address %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0.0.0.0:9090", "[::]:9090", "localhost:9090", "192.0.2.1:9090", "missing-port"} {
		if err := validateAdminListen(invalid); err == nil {
			t.Errorf("non-loopback address %q was accepted", invalid)
		}
	}
	if defaultAdminListen != "127.0.0.1:9090" {
		t.Fatalf("default admin listener = %q", defaultAdminListen)
	}
}

func TestAdminDashboardMetricsAndMethodBoundaries(t *testing.T) {
	observer, err := newObservability(defaultObservabilityConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	observer.publish(decisionEvent{Type: eventRequestStarted, RequestID: "dashboard-event-id"})
	address, stop := startAdminTestServer(t, observer, defaultAdminConfig())
	defer stop()

	response, err := stdhttp.Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != 200 || !strings.Contains(string(body), "ANVIL") || !strings.Contains(string(body), "new EventSource('/api/events')") {
		t.Fatalf("dashboard = status %d error %v body prefix %.80q", response.StatusCode, readErr, body)
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("dashboard security headers = %v", response.Header)
	}

	response, err = stdhttp.Get("http://" + address + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot metricsSnapshot
	decodeErr := json.NewDecoder(response.Body).Decode(&snapshot)
	_ = response.Body.Close()
	if decodeErr != nil || response.StatusCode != 200 || snapshot.Ledger.LatestSequence != 1 || len(snapshot.Latency.Buckets) == 0 || snapshot.Runtime.Goroutines == 0 || snapshot.Runtime.GOMAXPROCS == 0 || snapshot.Runtime.HeapObjectsBytes == 0 {
		t.Fatalf("metrics = status %d decode %v snapshot %+v", response.StatusCode, decodeErr, snapshot)
	}

	request, _ := stdhttp.NewRequest("POST", "http://"+address+"/api/metrics", nil)
	response, err = stdhttp.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 405 || response.Header.Get("Allow") != "GET" {
		t.Fatalf("admin POST = %d Allow %q", response.StatusCode, response.Header.Get("Allow"))
	}
	response, err = stdhttp.Get("http://" + address + "/api/mutate")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("unexpected mutation route status = %d", response.StatusCode)
	}
}

func TestSSEWireReplayIDsAndHeartbeat(t *testing.T) {
	observer, err := newObservability(defaultObservabilityConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	first := observer.publish(decisionEvent{Type: eventBackendSelected, BackendAlias: "alpha", Reason: "round-robin"})
	config := defaultAdminConfig()
	config.Heartbeat = 20 * time.Millisecond
	address, stop := startAdminTestServer(t, observer, config)
	defer stop()

	request, cancel := streamRequest(t, address, "")
	defer cancel()
	response, err := stdhttp.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
		t.Fatalf("SSE response = status %d content-type %q transfer %v", response.StatusCode, response.Header.Get("Content-Type"), response.TransferEncoding)
	}
	wire := readStreamUntil(t, response.Body, ": heartbeat", time.Second)
	for _, expected := range []string{": anvil stream ready", "id: 1", "event: backend_selected", `"backend_alias":"alpha"`, ": heartbeat"} {
		if !strings.Contains(wire, expected) {
			t.Fatalf("SSE wire missing %q: %q", expected, wire)
		}
	}
	if first.Sequence != 1 {
		t.Fatalf("published sequence = %d", first.Sequence)
	}
}

func TestSSERetainedReplayAndExpiredGap(t *testing.T) {
	config := defaultObservabilityConfig()
	config.LedgerCapacity = 2
	observer, err := newObservability(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	for range 4 {
		observer.publish(decisionEvent{Type: eventRequestCompleted, Status: 200})
	}
	address, stop := startAdminTestServer(t, observer, defaultAdminConfig())
	defer stop()

	request, cancel := streamRequest(t, address, "3")
	response, err := stdhttp.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	retained := readStreamUntil(t, response.Body, "id: 4", time.Second)
	_ = response.Body.Close()
	cancel()
	if strings.Contains(retained, "id: 3\n") || !strings.Contains(retained, "id: 4\n") || strings.Contains(retained, "event: gap") {
		t.Fatalf("retained replay = %q", retained)
	}

	request, cancel = streamRequest(t, address, "1")
	response, err = stdhttp.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	expired := readStreamUntil(t, response.Body, "id: 4", time.Second)
	_ = response.Body.Close()
	cancel()
	if !strings.Contains(expired, "event: gap") || !strings.Contains(expired, `"oldest_sequence":3`) || !strings.Contains(expired, "id: 3\n") || !strings.Contains(expired, "id: 4\n") {
		t.Fatalf("expired replay = %q", expired)
	}

	request, cancel = streamRequest(t, address, "99")
	response, err = stdhttp.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	future := readStreamUntil(t, response.Body, "id: 4", time.Second)
	_ = response.Body.Close()
	cancel()
	if !strings.Contains(future, "event: gap") || !strings.Contains(future, "id: 3\n") || !strings.Contains(future, "id: 4\n") {
		t.Fatalf("future replay = %q", future)
	}
}

func TestSSEInvalidLastEventIDAndSubscriberLimit(t *testing.T) {
	config := defaultObservabilityConfig()
	config.MaxSubscribers = 1
	observer, err := newObservability(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	address, stop := startAdminTestServer(t, observer, defaultAdminConfig())
	defer stop()

	invalid, _ := stdhttp.NewRequest("GET", "http://"+address+"/api/events", nil)
	invalid.Header.Set("Last-Event-ID", "not-a-number")
	response, err := stdhttp.DefaultClient.Do(invalid)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("invalid Last-Event-ID status = %d", response.StatusCode)
	}

	firstRequest, firstCancel := streamRequest(t, address, "")
	firstResponse, err := stdhttp.DefaultClient.Do(firstRequest)
	if err != nil {
		firstCancel()
		t.Fatal(err)
	}
	defer firstCancel()
	defer firstResponse.Body.Close()
	second, secondCancel := streamRequest(t, address, "")
	defer secondCancel()
	secondResponse, err := stdhttp.DefaultClient.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	_ = secondResponse.Body.Close()
	if secondResponse.StatusCode != 503 {
		t.Fatalf("subscriber saturation status = %d", secondResponse.StatusCode)
	}
}

func startAdminTestServer(t *testing.T, observer *observability, config adminConfig) (string, func()) {
	t.Helper()
	config.TCP.ShutdownTimeout = 200 * time.Millisecond
	config.TCP.ForceCloseWait = 200 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := newAdminServer(listener, config, observer)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case serveErr := <-done:
				if serveErr != nil {
					t.Errorf("admin server stop: %v", serveErr)
				}
			case <-time.After(3 * time.Second):
				t.Error("admin server did not stop")
			}
		})
	}
	return listener.Addr().String(), stop
}

func streamRequest(t *testing.T, address, lastEventID string) (*stdhttp.Request, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := stdhttp.NewRequestWithContext(ctx, "GET", "http://"+address+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	return request, cancel
}

func readStreamUntil(t *testing.T, body io.Reader, marker string, timeout time.Duration) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(body)
		var output strings.Builder
		for {
			line, err := reader.ReadString('\n')
			output.WriteString(line)
			if strings.Contains(output.String(), marker) || err != nil {
				result <- output.String()
				return
			}
		}
	}()
	select {
	case output := <-result:
		return output
	case <-time.After(timeout):
		if closer, ok := body.(io.Closer); ok {
			_ = closer.Close()
		}
		t.Fatal("timed out reading SSE stream")
		return ""
	}
}
