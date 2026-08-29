package main

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	defaultListen         = "127.0.0.1:8080"
	defaultMaxConnections = 256
	defaultReadTimeoutMS  = 10_000
	defaultWriteTimeoutMS = 10_000
	defaultIdleTimeoutMS  = 30_000
	defaultShutdownMS     = 5_000
	defaultForceCloseMS   = 1_000
	defaultMaxRequests    = defaultMaxRequestsPerConnection
	maxServerConnections  = 65_536
	maxRequestsPerClient  = 1_000_000
	maxConfiguredTimeout  = 24 * time.Hour
)

// Config contains the small runtime foundation shared by Anvil commands.
// Later phases will add proxy routes and upstream pools without changing the
// meaning of these admission and timeout fields.
type Config struct {
	Listen         string `json:"listen"`
	MaxConnections int    `json:"max_connections"`
	ReadTimeoutMS  int    `json:"read_timeout_ms"`
	WriteTimeoutMS int    `json:"write_timeout_ms"`
	IdleTimeoutMS  int    `json:"idle_timeout_ms"`
	ShutdownMS     int    `json:"shutdown_timeout_ms"`
	ForceCloseMS   int    `json:"force_close_timeout_ms"`
	MaxRequests    int    `json:"max_requests_per_connection"`
}

func DefaultConfig() Config {
	return Config{
		Listen:         defaultListen,
		MaxConnections: defaultMaxConnections,
		ReadTimeoutMS:  defaultReadTimeoutMS,
		WriteTimeoutMS: defaultWriteTimeoutMS,
		IdleTimeoutMS:  defaultIdleTimeoutMS,
		ShutdownMS:     defaultShutdownMS,
		ForceCloseMS:   defaultForceCloseMS,
		MaxRequests:    defaultMaxRequests,
	}
}

func (c Config) Validate() error {
	if len(c.Listen) > maxBackendAddressBytes {
		return fmt.Errorf("listen address exceeds %d bytes", maxBackendAddressBytes)
	}
	_, portText, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen address %q must be host:port: %w", c.Listen, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65_535 {
		return fmt.Errorf("listen port %q must be numeric and between 0 and 65535", portText)
	}
	if c.MaxConnections <= 0 || c.MaxConnections > maxServerConnections {
		return fmt.Errorf("max_connections must be between 1 and %d", maxServerConnections)
	}
	if !validMilliseconds(c.ReadTimeoutMS, maxConfiguredTimeout) {
		return fmt.Errorf("read_timeout_ms must be between 1 and %d", maxConfiguredTimeout/time.Millisecond)
	}
	if !validMilliseconds(c.WriteTimeoutMS, maxConfiguredTimeout) {
		return fmt.Errorf("write_timeout_ms must be between 1 and %d", maxConfiguredTimeout/time.Millisecond)
	}
	if !validMilliseconds(c.IdleTimeoutMS, maxConfiguredTimeout) {
		return fmt.Errorf("idle_timeout_ms must be between 1 and %d", maxConfiguredTimeout/time.Millisecond)
	}
	if !validMilliseconds(c.ShutdownMS, maxConfiguredTimeout) {
		return fmt.Errorf("shutdown_timeout_ms must be between 1 and %d", maxConfiguredTimeout/time.Millisecond)
	}
	if !validMilliseconds(c.ForceCloseMS, maxConfiguredTimeout) {
		return fmt.Errorf("force_close_timeout_ms must be between 1 and %d", maxConfiguredTimeout/time.Millisecond)
	}
	if c.MaxRequests <= 0 || c.MaxRequests > maxRequestsPerClient {
		return fmt.Errorf("max_requests_per_connection must be between 1 and %d", maxRequestsPerClient)
	}
	return nil
}

func validMilliseconds(value int, maximum time.Duration) bool {
	return value > 0 && int64(value) <= maximum.Milliseconds()
}

func (c Config) httpServerConfig() httpServerConfig {
	return httpServerConfig{
		Limits:                   defaultHTTPLimits(),
		MaxRequestsPerConnection: c.MaxRequests,
	}
}

func (c Config) tcpServerConfig() tcpServerConfig {
	return tcpServerConfig{
		MaxConnections:  c.MaxConnections,
		ReadTimeout:     time.Duration(c.ReadTimeoutMS) * time.Millisecond,
		WriteTimeout:    time.Duration(c.WriteTimeoutMS) * time.Millisecond,
		IdleTimeout:     time.Duration(c.IdleTimeoutMS) * time.Millisecond,
		ShutdownTimeout: time.Duration(c.ShutdownMS) * time.Millisecond,
		ForceCloseWait:  time.Duration(c.ForceCloseMS) * time.Millisecond,
	}
}
