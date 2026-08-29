package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultProxyDialTimeout   = 2 * time.Second
	defaultProxyReadTimeout   = 10 * time.Second
	defaultProxyWriteTimeout  = 10 * time.Second
	defaultBackendInFlight    = 128
	defaultViaName            = "anvil"
	requestIDHeader           = "X-Anvil-Request-ID"
	maxInformationalResponses = 8
)

type backendConfig struct {
	Alias       string
	Address     string
	Authority   string
	MaxInFlight int
}

func (c backendConfig) validate() error {
	if !validToken([]byte(c.Alias)) {
		return fmt.Errorf("backend alias %q must be an HTTP token", c.Alias)
	}
	host, portText, err := net.SplitHostPort(c.Address)
	if err != nil || host == "" {
		return fmt.Errorf("backend %q address %q must be host:port", c.Alias, c.Address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65_535 {
		return fmt.Errorf("backend %q port must be between 1 and 65535", c.Alias)
	}
	authority := c.Authority
	if authority == "" {
		authority = c.Address
	}
	if !validHost(authority) {
		return fmt.Errorf("backend %q authority %q is invalid", c.Alias, authority)
	}
	if c.MaxInFlight <= 0 {
		return fmt.Errorf("backend %q maximum in-flight requests must be greater than zero", c.Alias)
	}
	return nil
}

type proxyBackend struct {
	config    backendConfig
	admission chan struct{}
	inFlight  atomic.Int64
}

type backendReservation struct {
	backend  *proxyBackend
	released atomic.Bool
}

func (r *backendReservation) Release() {
	if r == nil || r.backend == nil || !r.released.CompareAndSwap(false, true) {
		return
	}
	r.backend.inFlight.Add(-1)
	<-r.backend.admission
}

type backendPool struct {
	backends []*proxyBackend
	sequence atomic.Uint64
}

func newBackendPool(configs []backendConfig) (*backendPool, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("backend pool requires at least one backend")
	}
	pool := &backendPool{backends: make([]*proxyBackend, 0, len(configs))}
	aliases := make(map[string]struct{}, len(configs))
	for _, config := range configs {
		if config.Authority == "" {
			config.Authority = config.Address
		}
		if err := config.validate(); err != nil {
			return nil, err
		}
		aliasKey := strings.ToLower(config.Alias)
		if _, exists := aliases[aliasKey]; exists {
			return nil, fmt.Errorf("backend alias %q is duplicated", config.Alias)
		}
		aliases[aliasKey] = struct{}{}
		pool.backends = append(pool.backends, &proxyBackend{
			config:    config,
			admission: make(chan struct{}, config.MaxInFlight),
		})
	}
	return pool, nil
}

func (p *backendPool) reserveNext() (*backendReservation, error) {
	if p == nil || len(p.backends) == 0 {
		return nil, &proxyError{Kind: proxyNoBackend}
	}
	start := int((p.sequence.Add(1) - 1) % uint64(len(p.backends)))
	for offset := range len(p.backends) {
		backend := p.backends[(start+offset)%len(p.backends)]
		select {
		case backend.admission <- struct{}{}:
			backend.inFlight.Add(1)
			return &backendReservation{backend: backend}, nil
		default:
		}
	}
	return nil, &proxyError{Kind: proxyAdmissionRejected}
}

type requestIDGenerator func() (string, error)

type proxyConfig struct {
	DialTimeout   time.Duration
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	Limits        httpLimits
	ViaName       string
	AddForwarded  bool
	AddXForwarded bool
	NewRequestID  requestIDGenerator
	DialContext   func(context.Context, string, string) (net.Conn, error)
}

func defaultProxyConfig() proxyConfig {
	return proxyConfig{
		DialTimeout:   defaultProxyDialTimeout,
		ReadTimeout:   defaultProxyReadTimeout,
		WriteTimeout:  defaultProxyWriteTimeout,
		Limits:        defaultHTTPLimits(),
		ViaName:       defaultViaName,
		AddForwarded:  true,
		AddXForwarded: true,
		NewRequestID:  randomRequestID,
	}
}

func (c *proxyConfig) setDefaults() {
	if c.NewRequestID == nil {
		c.NewRequestID = randomRequestID
	}
	if c.DialContext == nil {
		dialer := &net.Dialer{Timeout: c.DialTimeout}
		c.DialContext = dialer.DialContext
	}
}

func (c proxyConfig) validate() error {
	if c.DialTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 {
		return fmt.Errorf("proxy dial, read, and write timeouts must be greater than zero")
	}
	if err := c.Limits.validate(); err != nil {
		return err
	}
	if !validToken([]byte(c.ViaName)) {
		return fmt.Errorf("Via pseudonym %q must be an HTTP token", c.ViaName)
	}
	if c.NewRequestID == nil {
		return fmt.Errorf("request ID generator is required")
	}
	if c.DialContext == nil {
		return fmt.Errorf("dial function is required")
	}
	return nil
}

type proxyErrorKind string

const (
	proxyNoBackend         proxyErrorKind = "no_backend"
	proxyAdmissionRejected proxyErrorKind = "admission_rejected"
	proxyDialFailure       proxyErrorKind = "dial_failure"
	proxyDialTimeout       proxyErrorKind = "dial_timeout"
	proxyWriteFailure      proxyErrorKind = "write_failure"
	proxyUpstreamTimeout   proxyErrorKind = "upstream_timeout"
	proxyUpstreamProtocol  proxyErrorKind = "upstream_protocol"
	proxyCanceled          proxyErrorKind = "canceled"
)

type proxyError struct {
	Kind         proxyErrorKind
	BackendAlias string
	Err          error
}

func (e *proxyError) Error() string {
	message := "proxy error: " + string(e.Kind)
	if e.BackendAlias != "" {
		message += " (backend " + e.BackendAlias + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *proxyError) Unwrap() error { return e.Err }

type proxyAttempt struct {
	RequestID        string
	BackendAlias     string
	StartedAt        time.Time
	FinishedAt       time.Time
	Failure          proxyErrorKind
	DownstreamCommit *responseCommitState
	releaseOnce      sync.Once
}

func newProxyHandler(pool *backendPool, config proxyConfig) (routeHandler, error) {
	if pool == nil || len(pool.backends) == 0 {
		return nil, fmt.Errorf("backend pool is required")
	}
	config.setDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, request *httpRequest, _ routeParams) (*httpResponse, error) {
		return executeProxyRequest(ctx, request, pool, config), nil
	}, nil
}

func executeProxyRequest(ctx context.Context, request *httpRequest, pool *backendPool, config proxyConfig) *httpResponse {
	requestID, err := config.NewRequestID()
	if err != nil || !validRequestID(requestID) {
		response := textResponse(500, "internal server error\n")
		response.Close = true
		return response
	}
	commitState := &responseCommitState{}
	reservation, err := pool.reserveNext()
	if err != nil {
		return proxyFailureResponse(err, requestID, commitState)
	}
	attempt := &proxyAttempt{
		RequestID:        requestID,
		BackendAlias:     reservation.backend.config.Alias,
		StartedAt:        time.Now(),
		DownstreamCommit: commitState,
	}
	defer attempt.releaseOnce.Do(reservation.Release)

	response, err := executeProxyAttempt(ctx, request, reservation.backend, requestID, config)
	attempt.FinishedAt = time.Now()
	if err != nil {
		var failure *proxyError
		if errors.As(err, &failure) {
			attempt.Failure = failure.Kind
		}
		return proxyFailureResponse(err, requestID, commitState)
	}
	response.CommitState = commitState
	response.Headers = replaceHeader(response.Headers, requestIDHeader, requestID)
	return response
}

func executeProxyAttempt(ctx context.Context, request *httpRequest, backend *proxyBackend, requestID string, config proxyConfig) (*httpResponse, error) {
	connection, err := config.DialContext(ctx, "tcp", backend.config.Address)
	if err != nil {
		kind := proxyDialFailure
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			kind = proxyDialTimeout
		}
		return nil, &proxyError{Kind: kind, BackendAlias: backend.config.Alias, Err: err}
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()

	upstreamRequest := buildUpstreamRequest(request, backend.config.Authority, requestID, config)
	if err := connection.SetWriteDeadline(time.Now().Add(config.WriteTimeout)); err != nil {
		return nil, &proxyError{Kind: proxyWriteFailure, BackendAlias: backend.config.Alias, Err: err}
	}
	writer := bufio.NewWriter(connection)
	if err := writeHTTPRequest(writer, upstreamRequest); err != nil {
		return nil, classifyProxyWriteError(backend.config.Alias, err)
	}
	if err := writer.Flush(); err != nil {
		return nil, classifyProxyWriteError(backend.config.Alias, err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(config.ReadTimeout)); err != nil {
		return nil, &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: backend.config.Alias, Err: err}
	}

	reader := bufio.NewReader(connection)
	for informational := 0; informational <= maxInformationalResponses; informational++ {
		response, err := readHTTPResponse(reader, config.Limits, request.Method)
		if err != nil {
			return nil, classifyProxyReadError(ctx, backend.config.Alias, err)
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			if informational == maxInformationalResponses {
				return nil, &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: backend.config.Alias, Err: fmt.Errorf("too many informational responses")}
			}
			continue
		}
		return sanitizeUpstreamResponse(response, config.ViaName), nil
	}
	return nil, &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: backend.config.Alias, Err: fmt.Errorf("missing final response")}
}

func buildUpstreamRequest(request *httpRequest, authority, requestID string, config proxyConfig) *httpRequest {
	connectionFields := connectionNominations(request.Headers)
	remove := hopByHopFieldSet(connectionFields)
	for _, name := range []string{"Host", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", requestIDHeader} {
		remove[strings.ToLower(name)] = struct{}{}
	}
	headers := filterHeaderFields(request.Headers, remove)
	headers = append(headers,
		headerField{Name: "Host", Value: authority},
		headerField{Name: "Connection", Value: "close"},
		headerField{Name: "Via", Value: "1.1 " + config.ViaName},
		headerField{Name: requestIDHeader, Value: requestID},
	)
	peerIP, peerOK := remoteIP(request.RemoteAddr)
	originalHost, _ := request.Headers.First("Host")
	if peerOK && config.AddForwarded {
		headers = append(headers, headerField{Name: "Forwarded", Value: forwardedValue(peerIP, originalHost)})
	}
	if peerOK && config.AddXForwarded {
		headers = append(headers,
			headerField{Name: "X-Forwarded-For", Value: peerIP.String()},
			headerField{Name: "X-Forwarded-Host", Value: originalHost},
			headerField{Name: "X-Forwarded-Proto", Value: "http"},
		)
	}
	mode := request.BodyMode
	if mode == bodyModeCloseDelimited {
		mode = bodyModeFixed
	}
	return &httpRequest{
		Method:    request.Method,
		Target:    request.Target,
		Version:   httpVersion11,
		Headers:   headers,
		Trailers:  filterHeaderFields(request.Trailers, remove),
		Body:      request.Body,
		BodyMode:  mode,
		KeepAlive: false,
	}
}

func sanitizeUpstreamResponse(response *httpResponse, viaName string) *httpResponse {
	remove := hopByHopFieldSet(connectionNominations(response.Headers))
	remove[strings.ToLower(requestIDHeader)] = struct{}{}
	mode := response.BodyMode
	if mode == bodyModeCloseDelimited {
		mode = bodyModeFixed
	}
	headers := filterHeaderFields(response.Headers, remove)
	headers = append(headers, headerField{Name: "Via", Value: "1.1 " + viaName})
	return &httpResponse{
		Version:    httpVersion11,
		StatusCode: response.StatusCode,
		Reason:     response.Reason,
		Headers:    headers,
		Trailers:   filterHeaderFields(response.Trailers, remove),
		Body:       response.Body,
		BodyMode:   mode,
		KeepAlive:  true,
	}
}

func connectionNominations(fields headerFields) map[string]struct{} {
	nominated := make(map[string]struct{})
	for _, value := range fields.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			name := strings.TrimSpace(token)
			if validToken([]byte(name)) {
				nominated[strings.ToLower(name)] = struct{}{}
			}
		}
	}
	return nominated
}

func hopByHopFieldSet(nominated map[string]struct{}) map[string]struct{} {
	remove := make(map[string]struct{}, len(nominated)+9)
	for name := range nominated {
		remove[name] = struct{}{}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		remove[strings.ToLower(name)] = struct{}{}
	}
	return remove
}

func filterHeaderFields(fields headerFields, remove map[string]struct{}) headerFields {
	filtered := make(headerFields, 0, len(fields))
	for _, field := range fields {
		if _, blocked := remove[strings.ToLower(field.Name)]; blocked {
			continue
		}
		filtered = append(filtered, field)
	}
	return filtered
}

func replaceHeader(fields headerFields, name, value string) headerFields {
	remove := map[string]struct{}{strings.ToLower(name): {}}
	return append(filterHeaderFields(fields, remove), headerField{Name: name, Value: value})
}

func remoteIP(address string) (net.IP, bool) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, false
	}
	ip := net.ParseIP(host)
	return ip, ip != nil
}

func forwardedValue(ip net.IP, originalHost string) string {
	forValue := ip.String()
	if strings.Contains(forValue, ":") {
		forValue = "\"[" + forValue + "]\""
	}
	return "for=" + forValue + ";host=\"" + originalHost + "\";proto=http"
}

func randomRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return hex.EncodeToString(value[:]), nil
}

func validRequestID(value string) bool {
	return len(value) >= 16 && len(value) <= 64 && validToken([]byte(value))
}

func classifyProxyWriteError(alias string, err error) error {
	kind := proxyWriteFailure
	if isTimeout(err) {
		kind = proxyUpstreamTimeout
	}
	return &proxyError{Kind: kind, BackendAlias: alias, Err: err}
}

func classifyProxyReadError(ctx context.Context, alias string, err error) error {
	if ctx.Err() != nil {
		return &proxyError{Kind: proxyCanceled, BackendAlias: alias, Err: ctx.Err()}
	}
	if protocolKind(err) == protocolTimeout || isTimeout(err) {
		return &proxyError{Kind: proxyUpstreamTimeout, BackendAlias: alias, Err: err}
	}
	return &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: alias, Err: err}
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func proxyFailureResponse(err error, requestID string, commitState *responseCommitState) *httpResponse {
	status := 502
	message := "bad gateway\n"
	var failure *proxyError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case proxyNoBackend, proxyAdmissionRejected, proxyCanceled:
			status, message = 503, "service unavailable\n"
		case proxyDialTimeout, proxyUpstreamTimeout:
			status, message = 504, "gateway timeout\n"
		}
	}
	response := textResponse(status, message)
	response.Headers = append(response.Headers, headerField{Name: requestIDHeader, Value: requestID})
	response.CommitState = commitState
	return response
}
