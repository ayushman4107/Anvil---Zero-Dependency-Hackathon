package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventLedgerWrapReplayAndGap(t *testing.T) {
	ledger, err := newEventLedger(3)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 5; index++ {
		ledger.append(decisionEvent{Type: eventRequestCompleted, Status: 200 + index}, time.Duration(index)*time.Millisecond)
	}
	latest := ledger.snapshotSince(0)
	if latest.Capacity != 3 || latest.Count != 3 || latest.OldestSequence != 3 || latest.LatestSequence != 5 || latest.Gap {
		t.Fatalf("latest snapshot = %+v", latest)
	}
	if got := eventSequences(latest.Events); got != "3,4,5" {
		t.Fatalf("retained sequences = %s", got)
	}
	replay := ledger.snapshotSince(3)
	if replay.Gap || eventSequences(replay.Events) != "4,5" {
		t.Fatalf("retained replay = %+v", replay)
	}
	expired := ledger.snapshotSince(1)
	if !expired.Gap || eventSequences(expired.Events) != "3,4,5" {
		t.Fatalf("expired replay = %+v", expired)
	}
	future := ledger.snapshotSince(99)
	if !future.Gap || eventSequences(future.Events) != "3,4,5" {
		t.Fatalf("future replay = %+v", future)
	}
}

func TestSSEHubQueuesAreBoundedAndPublicationNeverBlocks(t *testing.T) {
	hub, err := newSSEHub(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer hub.unsubscribe(subscription.ID)
	if _, err := hub.subscribe(); err != errSubscriberLimit {
		t.Fatalf("subscriber saturation error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		for sequence := uint64(1); sequence <= 10_000; sequence++ {
			hub.publish(decisionEvent{Sequence: sequence, Type: eventRequestStarted})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("non-blocking hub publication stalled on a full subscriber")
	}
	_, dropped := hub.stats()
	if dropped == 0 {
		t.Fatal("full subscriber queue did not increment drops")
	}
	if event := <-subscription.Events; event.Sequence != 1 {
		t.Fatalf("queued event sequence = %d", event.Sequence)
	}
}

func TestLatencyHistogramProducesFixedBucketEstimates(t *testing.T) {
	histogram := &latencyHistogram{}
	for _, duration := range []time.Duration{50 * time.Microsecond, 900 * time.Microsecond, 20 * time.Millisecond, 80 * time.Millisecond, 2 * time.Second} {
		histogram.observe(duration)
	}
	snapshot := histogram.snapshot()
	if len(snapshot.Buckets) != len(latencyUpperBoundsMicros)+1 {
		t.Fatalf("bucket count = %d", len(snapshot.Buckets))
	}
	if snapshot.P50MS != 25 || snapshot.P95MS != 2_500 || snapshot.P99MS != 2_500 {
		t.Fatalf("percentile estimates = p50 %.1f p95 %.1f p99 %.1f", snapshot.P50MS, snapshot.P95MS, snapshot.P99MS)
	}
}

func TestProxyMetricsReconcileUnderConcurrency(t *testing.T) {
	metrics := newProxyMetrics()
	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 500 {
				metrics.beginRequest(3)
				metrics.recordAttempt()
				metrics.completeRequest(200, 7, time.Millisecond, false)
			}
		}()
	}
	workers.Wait()
	snapshot := metrics.requestSnapshot()
	if snapshot.Total != 16_000 || snapshot.Completed != 16_000 || snapshot.Successes != 16_000 || snapshot.Attempts != 16_000 || snapshot.Active != 0 || snapshot.RequestBytes != 48_000 || snapshot.ResponseBytes != 112_000 {
		t.Fatalf("request metrics = %+v", snapshot)
	}
}

func TestProxyEventsAreCausalAndExcludeSensitiveData(t *testing.T) {
	fixture, stopFixture := startProxyFixture(t, "safe", func(*httpRequest) *httpResponse { return textResponse(200, "ok") })
	defer stopFixture()
	pool, err := newBackendPool([]backendConfig{{Alias: "safe", Address: fixture, MaxInFlight: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	observer, err := newObservability(defaultObservabilityConfig(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	config := defaultProxyConfig()
	config.Observability = observer
	config.NewRequestID = func() (string, error) { return "phase6-private-event-id", nil }
	request := &httpRequest{
		Method: "POST", Target: "/", Version: httpVersion11, BodyMode: bodyModeFixed, Body: []byte("secret-body-value"),
		Headers:    headerFields{{Name: "Host", Value: "test"}, {Name: "Authorization", Value: "Bearer secret-token"}, {Name: "Cookie", Value: "session=secret-cookie"}},
		RemoteAddr: "127.0.0.1:4000",
	}
	response := executeProxyRequest(context.Background(), request, pool, config)
	if response.StatusCode != 200 {
		t.Fatalf("response status = %d", response.StatusCode)
	}
	snapshot := observer.ledger.snapshotSince(0)
	if eventSequences(snapshot.Events) != "1,2,3" || snapshot.Events[0].Type != eventRequestStarted || snapshot.Events[1].Type != eventBackendSelected || snapshot.Events[2].Type != eventRequestCompleted {
		t.Fatalf("events = %+v", snapshot.Events)
	}
	encoded, err := json.Marshal(snapshot.Events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-body-value", "secret-token", "secret-cookie", fixture, "127.0.0.1"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("event JSON leaked %q: %s", forbidden, encoded)
		}
	}
	metrics := observer.snapshot().Requests
	if metrics.Total != 1 || metrics.Completed != 1 || metrics.Attempts != 1 || metrics.Successes != 1 || metrics.RequestBytes != uint64(len(request.Body)) || metrics.ResponseBytes != 2 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestRetryLedgerAndMetricsExplainFailover(t *testing.T) {
	refused := unusedTCPAddress(t)
	fixture, stopFixture := startProxyFixture(t, "healthy", func(*httpRequest) *httpResponse { return textResponse(200, "ok") })
	defer stopFixture()
	pool, err := newBackendPool([]backendConfig{
		{Alias: "refused", Address: refused, MaxInFlight: 2},
		{Alias: "healthy", Address: fixture, MaxInFlight: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	observer, err := newObservability(defaultObservabilityConfig(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	config := defaultProxyConfig()
	config.Observability = observer
	config.NewRequestID = func() (string, error) { return "phase6-causal-retry-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}}
	response := executeProxyRequest(context.Background(), request, pool, config)
	if response.StatusCode != 200 {
		t.Fatalf("response status = %d", response.StatusCode)
	}
	events := observer.ledger.snapshotSince(0).Events
	wantTypes := []eventType{eventRequestStarted, eventBackendSelected, eventAttemptFailed, eventRetryScheduled, eventBackendSelected, eventRequestCompleted}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %+v", events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want || events[index].Sequence != uint64(index+1) {
			t.Fatalf("event %d = %+v, want %s", index, events[index], want)
		}
	}
	metrics := observer.snapshot()
	if metrics.Requests.Attempts != 2 || metrics.Requests.Retries != 1 || metrics.Failures.DialFailure != 1 || metrics.Requests.Successes != 1 {
		t.Fatalf("retry metrics = %+v / %+v", metrics.Requests, metrics.Failures)
	}
}

func TestGatewayErrorMetricDistinguishesForwardedStatus(t *testing.T) {
	fixture, stopFixture := startProxyFixture(t, "application", func(*httpRequest) *httpResponse { return textResponse(503, "busy") })
	defer stopFixture()
	pool, err := newBackendPool([]backendConfig{{Alias: "application", Address: fixture, MaxInFlight: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	observer, err := newObservability(defaultObservabilityConfig(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	config := defaultProxyConfig()
	config.Observability = observer
	config.NewRequestID = func() (string, error) { return "phase6-forwarded-503-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}}
	if response := executeProxyRequest(context.Background(), request, pool, config); response.StatusCode != 503 {
		t.Fatalf("forwarded response status = %d", response.StatusCode)
	}
	metrics := observer.snapshot().Requests
	if metrics.Status5xx != 1 || metrics.GatewayErrors != 0 {
		t.Fatalf("forwarded 503 metrics = %+v", metrics)
	}
}

func TestSlowSubscriberCannotBackPressureProxy(t *testing.T) {
	fixture, stopFixture := startProxyFixture(t, "healthy", func(*httpRequest) *httpResponse { return textResponse(200, "ok") })
	defer stopFixture()
	pool, err := newBackendPool([]backendConfig{{Alias: "healthy", Address: fixture, MaxInFlight: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	config := defaultObservabilityConfig()
	config.SubscriberQueue = 1
	observer, err := newObservability(config, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	subscriber, err := observer.hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer observer.hub.unsubscribe(subscriber.ID)
	proxyConfig := defaultProxyConfig()
	proxyConfig.Observability = observer
	proxyConfig.NewRequestID = func() (string, error) { return "phase6-slow-subscriber-id", nil }
	request := &httpRequest{Method: "GET", Target: "/", Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: "test"}}}
	done := make(chan *httpResponse, 1)
	go func() { done <- executeProxyRequest(context.Background(), request, pool, proxyConfig) }()
	select {
	case response := <-done:
		if response.StatusCode != 200 {
			t.Fatalf("response status = %d", response.StatusCode)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy was back-pressured by a full observer queue")
	}
	_, dropped := observer.hub.stats()
	if dropped == 0 {
		t.Fatal("slow subscriber did not register dropped events")
	}
}

func TestCircuitAndHealthTransitionsEnterLedgerOnce(t *testing.T) {
	observer, err := newObservability(defaultObservabilityConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	policy := defaultResilienceConfig()
	policy.PassiveFailureThreshold = 1
	policy.ActiveFailureThreshold = 1
	policy.OnCircuitTransition = observer.recordCircuitTransition
	policy.OnHealthTransition = observer.recordHealthTransition
	pool, err := newBackendPoolWithConfig([]backendConfig{{Alias: "node-a", Address: "127.0.0.1:8001", MaxInFlight: 1}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	observer.pool = pool
	reservation, err := pool.reserveNext()
	if err != nil {
		t.Fatal(err)
	}
	reservation.Complete(passiveFailure, policy.Now())
	pool.backends[0].recordActive(false)
	events := observer.ledger.snapshotSince(0).Events
	if len(events) != 2 || events[0].Type != eventCircuitTransition || events[0].PreviousState != "closed" || events[0].NewState != "open" || events[1].Type != eventHealthTransition || events[1].PreviousState != "healthy" || events[1].NewState != "unhealthy" {
		t.Fatalf("transition events = %+v", events)
	}
	metrics := observer.snapshot().Requests
	if metrics.CircuitChanges != 1 || metrics.HealthChanges != 1 {
		t.Fatalf("transition metrics = %+v", metrics)
	}
}

func eventSequences(events []decisionEvent) string {
	parts := make([]string, len(events))
	for index, event := range events {
		parts[index] = fmt.Sprint(event.Sequence)
	}
	return strings.Join(parts, ",")
}
