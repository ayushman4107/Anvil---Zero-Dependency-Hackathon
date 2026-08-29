package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type benchmarkConfig struct {
	Address       string
	Authority     string
	Method        string
	Path          string
	Workers       int
	MaxRequests   int
	RatePerSecond int
	Duration      time.Duration
	Timeout       time.Duration
	Limits        httpLimits
	DialContext   func(context.Context, string, string) (net.Conn, error)
}

type benchmarkResult struct {
	OfferedRequests uint64            `json:"offered_requests"`
	Completed       uint64            `json:"completed"`
	StatusCounts    map[string]uint64 `json:"status_counts"`
	ErrorCounts     map[string]uint64 `json:"error_counts"`
	RequestBytes    uint64            `json:"request_bytes"`
	ResponseBytes   uint64            `json:"response_bytes"`
	NewConnections  uint64            `json:"new_connections"`
	ReusedRequests  uint64            `json:"reused_requests"`
	PeakInFlight    int64             `json:"peak_in_flight"`
	ElapsedMicros   int64             `json:"elapsed_micros"`
	RequestsPerSec  float64           `json:"requests_per_second"`
	Latency         latencySnapshot   `json:"latency"`
}

type benchmarkCounters struct {
	offered        atomic.Uint64
	completed      atomic.Uint64
	requestBytes   atomic.Uint64
	responseBytes  atomic.Uint64
	newConnections atomic.Uint64
	reusedRequests atomic.Uint64
	inFlight       atomic.Int64
	peakInFlight   atomic.Int64
	latency        latencyHistogram
	mu             sync.Mutex
	statuses       map[string]uint64
	errors         map[string]uint64
}

type countingConnection struct {
	net.Conn
	readBytes  uint64
	writeBytes uint64
}

func (c *countingConnection) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.readBytes += uint64(n)
	return n, err
}

func (c *countingConnection) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	c.writeBytes += uint64(n)
	return n, err
}

func (c *benchmarkConfig) setDefaults() {
	if c.Method == "" {
		c.Method = "GET"
	}
	if c.Path == "" {
		c.Path = "/"
	}
	if c.Authority == "" {
		c.Authority = c.Address
	}
	if c.Limits.MaxStartLineBytes == 0 {
		c.Limits = defaultHTTPLimits()
	}
	if c.DialContext == nil {
		dialer := &net.Dialer{Timeout: c.Timeout}
		c.DialContext = dialer.DialContext
	}
}

func (c benchmarkConfig) validate() error {
	host, portText, err := net.SplitHostPort(c.Address)
	if err != nil || host == "" {
		return fmt.Errorf("benchmark address must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65_535 {
		return fmt.Errorf("benchmark port must be between 1 and 65535")
	}
	if !validHost(c.Authority) || (c.Method != "GET" && c.Method != "HEAD") || !validScenarioPath(c.Path) {
		return fmt.Errorf("benchmark authority, method, or path is invalid")
	}
	if c.Workers <= 0 || c.Workers > maxExperimentWorkers || c.MaxRequests <= 0 || c.MaxRequests > maxExperimentRequests {
		return fmt.Errorf("benchmark workers or request bound is invalid")
	}
	if c.RatePerSecond < 0 || c.RatePerSecond > 10_000 || c.Duration <= 0 || c.Duration > maxExperimentDuration || c.Timeout <= 0 || c.Timeout > time.Minute {
		return fmt.Errorf("benchmark rate, duration, or timeout is invalid")
	}
	if err := c.Limits.validate(); err != nil {
		return err
	}
	if c.DialContext == nil {
		return fmt.Errorf("benchmark dial function is required")
	}
	return nil
}

func runBenchmark(parent context.Context, config benchmarkConfig) (benchmarkResult, error) {
	if parent == nil {
		return benchmarkResult{}, fmt.Errorf("benchmark context is required")
	}
	config.setDefaults()
	if err := config.validate(); err != nil {
		return benchmarkResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, config.Duration)
	defer cancel()
	request := &httpRequest{Method: config.Method, Target: config.Path, Version: httpVersion11, Headers: headerFields{{Name: "Host", Value: config.Authority}}, BodyMode: bodyModeNone, KeepAlive: true}
	counters := &benchmarkCounters{statuses: make(map[string]uint64), errors: make(map[string]uint64)}
	jobs := make(chan struct{}, config.Workers)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(jobs)
		var ticker *time.Ticker
		var ticks <-chan time.Time
		if config.RatePerSecond > 0 {
			interval := time.Second / time.Duration(config.RatePerSecond)
			ticker = time.NewTicker(interval)
			ticks = ticker.C
			defer ticker.Stop()
		}
		for index := range config.MaxRequests {
			if ticks != nil && index > 0 {
				select {
				case <-ctx.Done():
					return
				case <-ticks:
				}
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- struct{}{}:
				counters.offered.Add(1)
			}
		}
	}()

	started := time.Now()
	var workers sync.WaitGroup
	workers.Add(config.Workers)
	for range config.Workers {
		go func() {
			defer workers.Done()
			runBenchmarkWorker(ctx, config, request, jobs, counters)
		}()
	}
	workers.Wait()
	<-producerDone
	elapsed := time.Since(started)
	completed := counters.completed.Load()
	result := benchmarkResult{
		OfferedRequests: counters.offered.Load(), Completed: completed,
		RequestBytes: counters.requestBytes.Load(), ResponseBytes: counters.responseBytes.Load(),
		NewConnections: counters.newConnections.Load(), ReusedRequests: counters.reusedRequests.Load(),
		PeakInFlight: counters.peakInFlight.Load(), ElapsedMicros: max(elapsed.Microseconds(), 0), Latency: counters.latency.snapshot(),
	}
	if elapsed > 0 {
		result.RequestsPerSec = float64(completed) / elapsed.Seconds()
	}
	counters.mu.Lock()
	result.StatusCounts = cloneCountMap(counters.statuses)
	result.ErrorCounts = cloneCountMap(counters.errors)
	counters.mu.Unlock()
	return result, nil
}

func runBenchmarkWorker(ctx context.Context, config benchmarkConfig, request *httpRequest, jobs <-chan struct{}, counters *benchmarkCounters) {
	var connection *countingConnection
	var reader *bufio.Reader
	var writer *bufio.Writer
	defer func() {
		if connection != nil {
			_ = connection.Close()
		}
	}()
	for range jobs {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		active := counters.inFlight.Add(1)
		recordAtomicPeak(&counters.peakInFlight, active)
		if connection == nil {
			raw, err := config.DialContext(ctx, "tcp", config.Address)
			if err != nil {
				counters.recordError(classifyBenchmarkError("dial", err), time.Since(started))
				counters.inFlight.Add(-1)
				continue
			}
			connection = &countingConnection{Conn: raw}
			reader, writer = bufio.NewReader(connection), bufio.NewWriter(connection)
			counters.newConnections.Add(1)
		} else {
			counters.reusedRequests.Add(1)
		}
		beforeRead, beforeWrite := connection.readBytes, connection.writeBytes
		currentConnection := connection
		stopCancellation := context.AfterFunc(ctx, func() { _ = currentConnection.Close() })
		deadline := time.Now().Add(config.Timeout)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = connection.SetDeadline(deadline)
		operation := "write"
		requestErr := writeHTTPRequest(writer, request)
		if requestErr == nil {
			requestErr = writer.Flush()
		}
		var response *httpResponse
		if requestErr == nil {
			operation = "read"
			response, requestErr = readFinalBenchmarkResponse(reader, config.Limits, request.Method)
		}
		stopCancellation()
		counters.requestBytes.Add(connection.writeBytes - beforeWrite)
		counters.responseBytes.Add(connection.readBytes - beforeRead)
		duration := time.Since(started)
		if requestErr != nil {
			counters.recordError(classifyBenchmarkError(operation, requestErr), duration)
			_ = connection.Close()
			connection, reader, writer = nil, nil, nil
		} else {
			counters.recordStatus(response.StatusCode, duration)
			if !response.KeepAlive || response.Close || response.BodyMode == bodyModeCloseDelimited {
				_ = connection.Close()
				connection, reader, writer = nil, nil, nil
			}
		}
		counters.inFlight.Add(-1)
	}
}

func readFinalBenchmarkResponse(reader *bufio.Reader, limits httpLimits, method string) (*httpResponse, error) {
	for informational := 0; informational <= maxInformationalResponses; informational++ {
		response, err := readHTTPResponse(reader, limits, method)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 100 || response.StatusCode >= 200 {
			return response, nil
		}
	}
	return nil, fmt.Errorf("too many informational responses")
}

func (c *benchmarkCounters) recordStatus(status int, duration time.Duration) {
	c.completed.Add(1)
	c.latency.observe(duration)
	c.mu.Lock()
	c.statuses[strconv.Itoa(status)]++
	c.mu.Unlock()
}

func (c *benchmarkCounters) recordError(kind string, duration time.Duration) {
	c.completed.Add(1)
	c.latency.observe(duration)
	c.mu.Lock()
	c.errors[kind]++
	c.mu.Unlock()
}

func classifyBenchmarkError(operation string, err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() || protocolKind(err) == protocolTimeout {
		return operation + "_timeout"
	}
	if kind := protocolKind(err); kind != "" {
		return "protocol_" + string(kind)
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "refused") {
		return "connection_refused"
	}
	return operation + "_failure"
}

func recordAtomicPeak(peak *atomic.Int64, value int64) {
	for {
		current := peak.Load()
		if value <= current || peak.CompareAndSwap(current, value) {
			return
		}
	}
}

func cloneCountMap(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
