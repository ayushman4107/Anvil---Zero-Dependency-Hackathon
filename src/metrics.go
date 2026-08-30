package main

import (
	"math"
	runtimemetrics "runtime/metrics"
	"sync/atomic"
	"time"
)

var latencyUpperBoundsMicros = [...]int64{
	100, 250, 500, 1_000, 2_500, 5_000, 10_000, 25_000, 50_000,
	100_000, 250_000, 500_000, 1_000_000, 2_500_000, 5_000_000, 10_000_000,
}

type latencyHistogram struct {
	buckets [len(latencyUpperBoundsMicros) + 1]atomic.Uint64
}

func (h *latencyHistogram) observe(duration time.Duration) {
	micros := max(duration.Microseconds(), 0)
	index := len(latencyUpperBoundsMicros)
	for candidate, upper := range latencyUpperBoundsMicros {
		if micros <= upper {
			index = candidate
			break
		}
	}
	h.buckets[index].Add(1)
}

type latencyBucketSnapshot struct {
	UpperBoundMicros int64  `json:"upper_bound_micros,omitempty"`
	Count            uint64 `json:"count"`
	Overflow         bool   `json:"overflow,omitempty"`
}

type latencySnapshot struct {
	Buckets []latencyBucketSnapshot `json:"buckets"`
	P50MS   float64                 `json:"p50_ms_estimate"`
	P95MS   float64                 `json:"p95_ms_estimate"`
	P99MS   float64                 `json:"p99_ms_estimate"`
}

func (h *latencyHistogram) snapshot() latencySnapshot {
	counts := make([]uint64, len(h.buckets))
	buckets := make([]latencyBucketSnapshot, len(h.buckets))
	for index := range h.buckets {
		counts[index] = h.buckets[index].Load()
		if index < len(latencyUpperBoundsMicros) {
			buckets[index] = latencyBucketSnapshot{UpperBoundMicros: latencyUpperBoundsMicros[index], Count: counts[index]}
		} else {
			buckets[index] = latencyBucketSnapshot{Count: counts[index], Overflow: true}
		}
	}
	return latencySnapshot{
		Buckets: buckets,
		P50MS:   percentileEstimateMS(counts, 0.50),
		P95MS:   percentileEstimateMS(counts, 0.95),
		P99MS:   percentileEstimateMS(counts, 0.99),
	}
}

func percentileEstimateMS(counts []uint64, percentile float64) float64 {
	var total uint64
	for _, count := range counts {
		total += count
	}
	if total == 0 {
		return 0
	}
	target := uint64(math.Ceil(float64(total) * percentile))
	var cumulative uint64
	for index, count := range counts {
		cumulative += count
		if cumulative < target {
			continue
		}
		if index < len(latencyUpperBoundsMicros) {
			return float64(latencyUpperBoundsMicros[index]) / 1_000
		}
		return float64(latencyUpperBoundsMicros[len(latencyUpperBoundsMicros)-1]) / 1_000
	}
	return 0
}

type proxyMetrics struct {
	requests           atomic.Uint64
	completed          atomic.Uint64
	attempts           atomic.Uint64
	retries            atomic.Uint64
	successes          atomic.Uint64
	gatewayErrors      atomic.Uint64
	requestBytes       atomic.Uint64
	responseBytes      atomic.Uint64
	activeRequests     atomic.Int64
	peakActive         atomic.Int64
	statusClasses      [6]atomic.Uint64
	failureKinds       [proxyFailureKindCount]atomic.Uint64
	circuitTransitions atomic.Uint64
	healthTransitions  atomic.Uint64
	latency            latencyHistogram
}

const proxyFailureKindCount = 11

func newProxyMetrics() *proxyMetrics { return &proxyMetrics{} }

func (m *proxyMetrics) beginRequest(requestBytes int) {
	if m == nil {
		return
	}
	m.requests.Add(1)
	if requestBytes > 0 {
		m.requestBytes.Add(uint64(requestBytes))
	}
	active := m.activeRequests.Add(1)
	for {
		peak := m.peakActive.Load()
		if active <= peak || m.peakActive.CompareAndSwap(peak, active) {
			break
		}
	}
}

func (m *proxyMetrics) recordAttempt() {
	if m != nil {
		m.attempts.Add(1)
	}
}

func (m *proxyMetrics) recordRetry() {
	if m != nil {
		m.retries.Add(1)
	}
}

func (m *proxyMetrics) recordFailure(kind proxyErrorKind) {
	if m == nil {
		return
	}
	index := proxyFailureMetricIndex(kind)
	if index >= 0 {
		m.failureKinds[index].Add(1)
	}
}

func (m *proxyMetrics) completeRequest(status, responseBytes int, duration time.Duration, generatedGatewayError bool) {
	if m == nil {
		return
	}
	m.completed.Add(1)
	m.activeRequests.Add(-1)
	if responseBytes > 0 {
		m.responseBytes.Add(uint64(responseBytes))
	}
	class := status / 100
	if class >= 1 && class <= 5 {
		m.statusClasses[class].Add(1)
	}
	if status >= 200 && status < 400 {
		m.successes.Add(1)
	}
	if generatedGatewayError {
		m.gatewayErrors.Add(1)
	}
	m.latency.observe(duration)
}

func proxyFailureMetricIndex(kind proxyErrorKind) int {
	switch kind {
	case proxyNoBackend:
		return 0
	case proxyAdmissionRejected:
		return 1
	case proxyDialFailure:
		return 2
	case proxyDialTimeout:
		return 3
	case proxyWriteFailure:
		return 4
	case proxyWriteTimeout:
		return 5
	case proxyUpstreamTimeout:
		return 6
	case proxyUpstreamProtocol:
		return 7
	case proxyUpstreamIncomplete:
		return 8
	case proxyCanceled:
		return 9
	default:
		return 10
	}
}

type requestMetricSnapshot struct {
	Total          uint64 `json:"total"`
	Completed      uint64 `json:"completed"`
	Attempts       uint64 `json:"attempts"`
	Retries        uint64 `json:"retries"`
	Successes      uint64 `json:"successes"`
	GatewayErrors  uint64 `json:"gateway_errors"`
	RequestBytes   uint64 `json:"request_body_bytes"`
	ResponseBytes  uint64 `json:"response_body_bytes"`
	Active         int64  `json:"active"`
	PeakActive     int64  `json:"peak_active"`
	Status1xx      uint64 `json:"status_1xx"`
	Status2xx      uint64 `json:"status_2xx"`
	Status3xx      uint64 `json:"status_3xx"`
	Status4xx      uint64 `json:"status_4xx"`
	Status5xx      uint64 `json:"status_5xx"`
	CircuitChanges uint64 `json:"circuit_transitions"`
	HealthChanges  uint64 `json:"health_transitions"`
}

type failureMetricSnapshot struct {
	NoBackend          uint64 `json:"no_backend"`
	AdmissionRejected  uint64 `json:"admission_rejected"`
	DialFailure        uint64 `json:"dial_failure"`
	DialTimeout        uint64 `json:"dial_timeout"`
	WriteFailure       uint64 `json:"write_failure"`
	WriteTimeout       uint64 `json:"write_timeout"`
	UpstreamTimeout    uint64 `json:"upstream_timeout"`
	UpstreamProtocol   uint64 `json:"upstream_protocol"`
	UpstreamIncomplete uint64 `json:"upstream_incomplete"`
	Canceled           uint64 `json:"canceled"`
	Other              uint64 `json:"other"`
}

type runtimeMetricSnapshot struct {
	HeapObjectsBytes uint64  `json:"heap_objects_bytes"`
	TotalAllocBytes  uint64  `json:"total_alloc_bytes"`
	GCCycles         uint64  `json:"gc_cycles"`
	Goroutines       uint64  `json:"goroutines"`
	GOMAXPROCS       float64 `json:"gomaxprocs"`
}

type subscriberMetricSnapshot struct {
	Active  int    `json:"active"`
	Dropped uint64 `json:"dropped_events"`
}

type metricsSnapshot struct {
	ElapsedMillis int64                    `json:"elapsed_millis"`
	Requests      requestMetricSnapshot    `json:"requests"`
	Failures      failureMetricSnapshot    `json:"failures"`
	Latency       latencySnapshot          `json:"latency"`
	Backends      []backendSnapshot        `json:"backends"`
	Runtime       runtimeMetricSnapshot    `json:"runtime"`
	Subscribers   subscriberMetricSnapshot `json:"subscribers"`
	Ledger        ledgerSnapshot           `json:"ledger"`
	ProxyServer   serverStats              `json:"proxy_server"`
	AdminServer   serverStats              `json:"admin_server"`
}

func (o *observability) snapshot() metricsSnapshot {
	requestMetrics := o.metrics.requestSnapshot()
	failureMetrics := o.metrics.failureSnapshot()
	subscribers, dropped := o.hub.stats()
	ledger := o.ledger.snapshotSince(0)
	ledger.Events = nil
	proxyServer, adminServer := o.serverSnapshots()
	var backends []backendSnapshot
	if o.pool != nil {
		backends = o.pool.snapshots()
	}
	return metricsSnapshot{
		ElapsedMillis: max(o.now().Sub(o.startedAt).Milliseconds(), 0),
		Requests:      requestMetrics,
		Failures:      failureMetrics,
		Latency:       o.metrics.latency.snapshot(),
		Backends:      backends,
		Runtime:       readRuntimeMetrics(),
		Subscribers:   subscriberMetricSnapshot{Active: subscribers, Dropped: dropped},
		Ledger:        ledger,
		ProxyServer:   proxyServer,
		AdminServer:   adminServer,
	}
}

func (m *proxyMetrics) requestSnapshot() requestMetricSnapshot {
	return requestMetricSnapshot{
		Total:          m.requests.Load(),
		Completed:      m.completed.Load(),
		Attempts:       m.attempts.Load(),
		Retries:        m.retries.Load(),
		Successes:      m.successes.Load(),
		GatewayErrors:  m.gatewayErrors.Load(),
		RequestBytes:   m.requestBytes.Load(),
		ResponseBytes:  m.responseBytes.Load(),
		Active:         m.activeRequests.Load(),
		PeakActive:     m.peakActive.Load(),
		Status1xx:      m.statusClasses[1].Load(),
		Status2xx:      m.statusClasses[2].Load(),
		Status3xx:      m.statusClasses[3].Load(),
		Status4xx:      m.statusClasses[4].Load(),
		Status5xx:      m.statusClasses[5].Load(),
		CircuitChanges: m.circuitTransitions.Load(),
		HealthChanges:  m.healthTransitions.Load(),
	}
}

func (m *proxyMetrics) failureSnapshot() failureMetricSnapshot {
	return failureMetricSnapshot{
		NoBackend:          m.failureKinds[0].Load(),
		AdmissionRejected:  m.failureKinds[1].Load(),
		DialFailure:        m.failureKinds[2].Load(),
		DialTimeout:        m.failureKinds[3].Load(),
		WriteFailure:       m.failureKinds[4].Load(),
		WriteTimeout:       m.failureKinds[5].Load(),
		UpstreamTimeout:    m.failureKinds[6].Load(),
		UpstreamProtocol:   m.failureKinds[7].Load(),
		UpstreamIncomplete: m.failureKinds[8].Load(),
		Canceled:           m.failureKinds[9].Load(),
		Other:              m.failureKinds[10].Load(),
	}
}

func readRuntimeMetrics() runtimeMetricSnapshot {
	names := [...]string{
		"/memory/classes/heap/objects:bytes",
		"/gc/heap/allocs:bytes",
		"/gc/cycles/total:gc-cycles",
		"/sched/goroutines:goroutines",
		"/sched/gomaxprocs:threads",
	}
	samples := make([]runtimemetrics.Sample, len(names))
	for index, name := range names {
		samples[index].Name = name
	}
	runtimemetrics.Read(samples)
	result := runtimeMetricSnapshot{}
	result.HeapObjectsBytes = runtimeUint64(samples[0].Value)
	result.TotalAllocBytes = runtimeUint64(samples[1].Value)
	result.GCCycles = runtimeUint64(samples[2].Value)
	result.Goroutines = runtimeUint64(samples[3].Value)
	result.GOMAXPROCS = runtimeFloat64(samples[4].Value)
	return result
}

func runtimeUint64(value runtimemetrics.Value) uint64 {
	if value.Kind() == runtimemetrics.KindUint64 {
		return value.Uint64()
	}
	return 0
}

func runtimeFloat64(value runtimemetrics.Value) float64 {
	switch value.Kind() {
	case runtimemetrics.KindUint64:
		return float64(value.Uint64())
	case runtimemetrics.KindFloat64:
		return value.Float64()
	default:
		return 0
	}
}
