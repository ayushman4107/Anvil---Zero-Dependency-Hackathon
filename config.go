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
	}
}

func (c Config) Validate() error {
	_, portText, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen address %q must be host:port: %w", c.Listen, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65_535 {
		return fmt.Errorf("listen port %q must be numeric and between 0 and 65535", portText)
	}
	if c.MaxConnections <= 0 {
		return fmt.Errorf("max_connections must be greater than zero")
	}
	if c.ReadTimeoutMS <= 0 {
		return fmt.Errorf("read_timeout_ms must be greater than zero")
	}
	if c.WriteTimeoutMS <= 0 {
		return fmt.Errorf("write_timeout_ms must be greater than zero")
	}
	if c.IdleTimeoutMS <= 0 {
		return fmt.Errorf("idle_timeout_ms must be greater than zero")
	}
	if c.ShutdownMS <= 0 {
		return fmt.Errorf("shutdown_timeout_ms must be greater than zero")
	}
	if c.ForceCloseMS <= 0 {
		return fmt.Errorf("force_close_timeout_ms must be greater than zero")
	}
	return nil
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
