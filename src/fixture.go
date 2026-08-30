package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

type fixtureMode string

const (
	fixtureHealthy     fixtureMode = "healthy"
	fixtureDelayed     fixtureMode = "delayed"
	fixtureFailure     fixtureMode = "failure"
	fixtureTruncated   fixtureMode = "truncated"
	fixtureUnavailable fixtureMode = "unavailable"
	fixtureRecovered   fixtureMode = "recovered"
)

type fixtureProfile struct {
	Mode          fixtureMode
	Delay         time.Duration
	FailureStatus int
}

type fixtureSnapshot struct {
	Alias      string      `json:"alias"`
	Address    string      `json:"-"`
	Mode       fixtureMode `json:"mode"`
	Requests   uint64      `json:"requests"`
	Responses  uint64      `json:"responses"`
	Active     int64       `json:"active"`
	PeakActive int64       `json:"peak_active"`
}

type fixtureBackend struct {
	alias     string
	address   string
	listener  net.Listener
	server    *tcpServer
	profile   atomic.Pointer[fixtureProfile]
	requests  atomic.Uint64
	responses atomic.Uint64
	active    atomic.Int64
	peak      atomic.Int64
}

func validateFixtureProfile(mode fixtureMode, delayMS, status int) error {
	switch mode {
	case fixtureHealthy, fixtureRecovered, fixtureTruncated, fixtureUnavailable:
		if delayMS != 0 || status != 0 {
			return fmt.Errorf("mode %q does not accept delay_ms or failure_status", mode)
		}
	case fixtureDelayed:
		if delayMS <= 0 || delayMS > 60_000 || status != 0 {
			return fmt.Errorf("delayed mode requires delay_ms between 1 and 60000 and no failure_status")
		}
	case fixtureFailure:
		if status < 400 || status > 599 || delayMS != 0 {
			return fmt.Errorf("failure mode requires failure_status between 400 and 599 and no delay_ms")
		}
	default:
		return fmt.Errorf("unsupported fixture mode %q", mode)
	}
	return nil
}

func profileFromFixtureSpec(spec fixtureSpec) fixtureProfile {
	return fixtureProfile{Mode: spec.InitialMode, Delay: time.Duration(spec.DelayMS) * time.Millisecond, FailureStatus: spec.FailureStatus}
}

func profileFromScenarioStep(step scenarioStep) fixtureProfile {
	return fixtureProfile{Mode: step.Mode, Delay: time.Duration(step.DelayMS) * time.Millisecond, FailureStatus: step.FailureStatus}
}

func newFixtureBackend(spec fixtureSpec) (*fixtureBackend, error) {
	if !validToken([]byte(spec.Alias)) || len(spec.Alias) > maxBackendAliasBytes {
		return nil, fmt.Errorf("fixture alias must be an HTTP token")
	}
	if err := validateFixtureProfile(spec.InitialMode, spec.DelayMS, spec.FailureStatus); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	fixture := &fixtureBackend{alias: spec.Alias, address: listener.Addr().String(), listener: listener}
	profile := profileFromFixtureSpec(spec)
	fixture.profile.Store(&profile)
	config := DefaultConfig()
	config.Listen = fixture.address
	config.MaxConnections = 128
	config.MaxRequests = 1_000
	server, err := newTCPServer(listener, config.tcpServerConfig(), fixture.connectionHandler(config.httpServerConfig()))
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	fixture.server = server
	return fixture, nil
}

func (f *fixtureBackend) connectionHandler(config httpServerConfig) connectionHandler {
	return func(ctx context.Context, connection *clientConn) error {
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		for requestNumber := 1; requestNumber <= config.MaxRequestsPerConnection; requestNumber++ {
			request, err := readHTTPRequest(reader, config.Limits)
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return nil
				}
				response := protocolFailureResponse(err)
				_, writeErr := writeBufferedHTTPResponse(writer, response, "")
				return writeErr
			}
			f.requests.Add(1)
			active := f.active.Add(1)
			f.recordPeak(active)
			profile := *f.profile.Load()
			if profile.Mode == fixtureUnavailable {
				f.active.Add(-1)
				return nil
			}
			if profile.Mode == fixtureDelayed {
				timer := time.NewTimer(profile.Delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					f.active.Add(-1)
					return nil
				case <-timer.C:
				}
			}
			if profile.Mode == fixtureTruncated {
				f.responses.Add(1)
				f.active.Add(-1)
				if err := writeAll(writer, []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 64\r\nConnection: close\r\n\r\ntruncated")); err != nil {
					return err
				}
				return writer.Flush()
			}
			status := 200
			if profile.Mode == fixtureFailure {
				status = profile.FailureStatus
			}
			response := textResponse(status, fmt.Sprintf("fixture=%s mode=%s\n", f.alias, profile.Mode))
			response.Headers = append(response.Headers,
				headerField{Name: "X-Anvil-Fixture", Value: f.alias},
				headerField{Name: "X-Anvil-Fixture-Mode", Value: string(profile.Mode)},
			)
			closeAfter := !request.KeepAlive || requestNumber == config.MaxRequestsPerConnection
			response.Close = closeAfter
			response.KeepAlive = !closeAfter
			_, writeErr := writeBufferedHTTPResponse(writer, response, request.Method)
			f.responses.Add(1)
			f.active.Add(-1)
			if writeErr != nil || closeAfter {
				return writeErr
			}
		}
		return nil
	}
}

func (f *fixtureBackend) recordPeak(active int64) {
	for {
		peak := f.peak.Load()
		if active <= peak || f.peak.CompareAndSwap(peak, active) {
			return
		}
	}
}

func (f *fixtureBackend) apply(profile fixtureProfile) fixtureProfile {
	previous := *f.profile.Load()
	copy := profile
	f.profile.Store(&copy)
	return previous
}

func (f *fixtureBackend) mode() fixtureMode {
	if f == nil || f.profile.Load() == nil {
		return ""
	}
	return f.profile.Load().Mode
}

func (f *fixtureBackend) snapshot() fixtureSnapshot {
	return fixtureSnapshot{Alias: f.alias, Address: f.address, Mode: f.mode(), Requests: f.requests.Load(), Responses: f.responses.Load(), Active: f.active.Load(), PeakActive: f.peak.Load()}
}
