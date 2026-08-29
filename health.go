package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	defaultHealthInterval = 2 * time.Second
	defaultHealthTimeout  = 750 * time.Millisecond
)

type activeHealthConfig struct {
	Interval time.Duration
	Timeout  time.Duration
}

func defaultActiveHealthConfig() activeHealthConfig {
	return activeHealthConfig{Interval: defaultHealthInterval, Timeout: defaultHealthTimeout}
}

func (c activeHealthConfig) validate() error {
	if c.Interval <= 0 || c.Timeout <= 0 {
		return fmt.Errorf("health interval and timeout must be greater than zero")
	}
	return nil
}

type activeHealthChecker struct {
	pool        *backendPool
	proxyConfig proxyConfig
	config      activeHealthConfig

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wait    sync.WaitGroup
}

func newActiveHealthChecker(pool *backendPool, proxyConfig proxyConfig, config activeHealthConfig) (*activeHealthChecker, error) {
	if pool == nil || len(pool.backends) == 0 {
		return nil, fmt.Errorf("health checker requires a backend pool")
	}
	proxyConfig.setDefaults()
	if err := proxyConfig.validate(); err != nil {
		return nil, err
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &activeHealthChecker{pool: pool, proxyConfig: proxyConfig, config: config}, nil
}

func (h *activeHealthChecker) Start(parent context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return fmt.Errorf("health checker is already running")
	}
	ctx, cancel := context.WithCancel(parent)
	h.cancel = cancel
	h.running = true
	for _, backend := range h.pool.backends {
		h.wait.Add(1)
		go h.runBackend(ctx, backend)
	}
	return nil
}

func (h *activeHealthChecker) Stop() {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	cancel := h.cancel
	h.running = false
	h.cancel = nil
	h.mu.Unlock()
	cancel()
	h.wait.Wait()
}

func (h *activeHealthChecker) runBackend(ctx context.Context, backend *proxyBackend) {
	defer h.wait.Done()
	h.runProbe(ctx, backend)
	ticker := time.NewTicker(h.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runProbe(ctx, backend)
		}
	}
}

func (h *activeHealthChecker) runProbe(parent context.Context, backend *proxyBackend) bool {
	now := h.proxyConfig.Now()
	circuitProbe, transition := backend.beginCircuitProbe(now)
	backend.publishTransition(transition)
	ctx, cancel := context.WithTimeout(parent, h.config.Timeout)
	success := h.probe(ctx, backend)
	cancel()
	backend.recordActive(success)
	backend.completeCircuitProbe(success, circuitProbe, h.proxyConfig.Now())
	return success
}

func (h *activeHealthChecker) probe(ctx context.Context, backend *proxyBackend) bool {
	connection, err := h.proxyConfig.DialContext(ctx, "tcp", backend.config.Address)
	if err != nil {
		return false
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	deadline := time.Now().Add(h.config.Timeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return false
	}
	request := &httpRequest{
		Method:    "GET",
		Target:    backend.config.HealthPath,
		Version:   httpVersion11,
		Headers:   headerFields{{Name: "Host", Value: backend.config.Authority}, {Name: "Connection", Value: "close"}, {Name: "Via", Value: "1.1 " + h.proxyConfig.ViaName}},
		BodyMode:  bodyModeNone,
		KeepAlive: false,
	}
	writer := bufio.NewWriter(connection)
	if err := writeHTTPRequest(writer, request); err != nil {
		return false
	}
	if err := writer.Flush(); err != nil {
		return false
	}
	response, err := readFinalHealthResponse(bufio.NewReader(connection), h.proxyConfig.Limits)
	return err == nil && response.StatusCode >= 200 && response.StatusCode < 400
}

func readFinalHealthResponse(reader *bufio.Reader, limits httpLimits) (*httpResponse, error) {
	for informational := 0; informational <= maxInformationalResponses; informational++ {
		response, err := readHTTPResponse(reader, limits, "GET")
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			continue
		}
		return response, nil
	}
	return nil, &net.OpError{Op: "read", Net: "tcp", Err: fmt.Errorf("too many informational health responses")}
}
