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
	"time"
)

const (
	defaultProxyDialTimeout   = 2 * time.Second
	defaultProxyReadTimeout   = 10 * time.Second
	defaultProxyWriteTimeout  = 10 * time.Second
	defaultProxyRetryTimeout  = 2 * time.Second
	defaultProxyMaxAttempts   = 2
	defaultViaName            = "anvil"
	requestIDHeader           = "X-Anvil-Request-ID"
	maxInformationalResponses = 8
)

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
	if c.MaxIdleConnections < 0 || c.IdleTimeout <= 0 {
		return fmt.Errorf("backend %q idle connection limit must not be negative and idle timeout must be positive", c.Alias)
	}
	if c.HealthPath == "" || c.HealthPath[0] != '/' || strings.ContainsAny(c.HealthPath, "\r\n#") {
		return fmt.Errorf("backend %q health path must be a safe origin-form path", c.Alias)
	}
	return nil
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
	MaxAttempts   int
	RetryTimeout  time.Duration
	RetryStatuses map[int]struct{}
	Now           func() time.Time
	RouteAlias    string
	Observability *observability
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
		MaxAttempts:   defaultProxyMaxAttempts,
		RetryTimeout:  defaultProxyRetryTimeout,
		Now:           time.Now,
		RouteAlias:    "dev-proxy",
		NewRequestID:  randomRequestID,
	}
}

func (c *proxyConfig) setDefaults() {
	if c.NewRequestID == nil {
		c.NewRequestID = randomRequestID
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.RouteAlias == "" {
		c.RouteAlias = "dev-proxy"
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
	if c.MaxAttempts <= 0 || c.RetryTimeout <= 0 {
		return fmt.Errorf("proxy maximum attempts and retry timeout must be greater than zero")
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
	if c.Now == nil {
		return fmt.Errorf("proxy clock is required")
	}
	if !validToken([]byte(c.RouteAlias)) {
		return fmt.Errorf("route alias %q must be an HTTP token", c.RouteAlias)
	}
	for status := range c.RetryStatuses {
		if status < 400 || status > 599 {
			return fmt.Errorf("retry status %d must be between 400 and 599", status)
		}
	}
	return nil
}

type proxyErrorKind string

const (
	proxyNoBackend          proxyErrorKind = "no_backend"
	proxyAdmissionRejected  proxyErrorKind = "admission_rejected"
	proxyDialFailure        proxyErrorKind = "dial_failure"
	proxyDialTimeout        proxyErrorKind = "dial_timeout"
	proxyWriteFailure       proxyErrorKind = "write_failure"
	proxyWriteTimeout       proxyErrorKind = "write_timeout"
	proxyUpstreamTimeout    proxyErrorKind = "upstream_timeout"
	proxyUpstreamProtocol   proxyErrorKind = "upstream_protocol"
	proxyUpstreamIncomplete proxyErrorKind = "upstream_incomplete"
	proxyCanceled           proxyErrorKind = "canceled"
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

func executeProxyRequest(ctx context.Context, request *httpRequest, pool *backendPool, config proxyConfig) (finalResponse *httpResponse) {
	config.setDefaults()
	requestID, err := config.NewRequestID()
	if err != nil || !validRequestID(requestID) {
		response := textResponse(500, "internal server error\n")
		response.Close = true
		return response
	}
	requestStartedAt := config.Now()
	finalBackendAlias := ""
	if config.Observability != nil {
		config.Observability.metrics.beginRequest(len(request.Body))
		config.Observability.publish(decisionEvent{Type: eventRequestStarted, RequestID: requestID, RouteAlias: config.RouteAlias})
		defer func() {
			status := 500
			responseBytes := 0
			generatedGatewayError := true
			if finalResponse != nil {
				status = finalResponse.StatusCode
				responseBytes = len(finalResponse.Body)
				proxyStatus, _ := finalResponse.Headers.First("Proxy-Status")
				generatedGatewayError = strings.Contains(proxyStatus, "; error=")
			}
			duration := config.Now().Sub(requestStartedAt)
			config.Observability.metrics.completeRequest(status, responseBytes, duration, generatedGatewayError)
			config.Observability.publish(decisionEvent{
				Type:           eventRequestCompleted,
				RequestID:      requestID,
				RouteAlias:     config.RouteAlias,
				BackendAlias:   finalBackendAlias,
				Status:         status,
				DurationMicros: max(duration.Microseconds(), 0),
			})
		}()
	}
	commitState := &responseCommitState{}
	deadline := config.Now().Add(config.RetryTimeout)
	attempted := make(map[string]struct{}, config.MaxAttempts)
	var lastFailure error
	var lastRetryResponse *httpResponse
	var lastRetryAlias string
	for attemptNumber := 1; attemptNumber <= config.MaxAttempts; attemptNumber++ {
		reservation, reserveErr := pool.reserveNextExcluding(attempted)
		if reserveErr != nil {
			recordProxyAttemptFailure(config.Observability, requestID, config.RouteAlias, "", attemptNumber, reserveErr, 0)
			if lastRetryResponse != nil {
				finalBackendAlias = lastRetryAlias
				return finalizeProxyResponse(lastRetryResponse, requestID, lastRetryAlias, commitState, config.ViaName)
			}
			if lastFailure != nil {
				return proxyFailureResponse(lastFailure, requestID, commitState, config.ViaName)
			}
			return proxyFailureResponse(reserveErr, requestID, commitState, config.ViaName)
		}
		alias := reservation.backend.config.Alias
		attempted[strings.ToLower(alias)] = struct{}{}
		startedAt := config.Now()
		if config.Observability != nil {
			config.Observability.metrics.recordAttempt()
			config.Observability.publish(decisionEvent{Type: eventBackendSelected, RequestID: requestID, RouteAlias: config.RouteAlias, BackendAlias: alias, Reason: string(pool.resilience.Selector), Attempt: attemptNumber})
		}
		remaining := deadline.Sub(startedAt)
		attemptContext, cancelAttempt := context.WithTimeout(ctx, remaining)
		response, attemptErr := executeProxyAttempt(attemptContext, request, reservation.backend, requestID, config)
		cancelAttempt()
		finishedAt := config.Now()
		if attemptErr != nil {
			reservation.Complete(passiveOutcomeForError(attemptErr), finishedAt)
			recordProxyAttemptFailure(config.Observability, requestID, config.RouteAlias, alias, attemptNumber, attemptErr, finishedAt.Sub(startedAt))
			lastFailure = attemptErr
			if canRetryProxyRequest(request, attemptNumber, config.MaxAttempts, commitState, finishedAt, deadline) {
				recordProxyRetry(config.Observability, requestID, config.RouteAlias, alias, attemptNumber, "transport_failure")
				continue
			}
			return proxyFailureResponse(attemptErr, requestID, commitState, config.ViaName)
		}

		_, retryStatus := config.RetryStatuses[response.StatusCode]
		outcome := passiveSuccess
		if retryStatus || (pool.resilience.SlowLatencyThreshold > 0 && finishedAt.Sub(startedAt) >= pool.resilience.SlowLatencyThreshold) {
			outcome = passiveFailure
		}
		reservation.Complete(outcome, finishedAt)
		if retryStatus && canRetryProxyRequest(request, attemptNumber, config.MaxAttempts, commitState, finishedAt, deadline) {
			if config.Observability != nil {
				config.Observability.publish(decisionEvent{Type: eventAttemptFailed, RequestID: requestID, RouteAlias: config.RouteAlias, BackendAlias: alias, Reason: fmt.Sprintf("status_%d", response.StatusCode), Attempt: attemptNumber, Status: response.StatusCode, DurationMicros: max(finishedAt.Sub(startedAt).Microseconds(), 0)})
			}
			recordProxyRetry(config.Observability, requestID, config.RouteAlias, alias, attemptNumber, "configured_status")
			lastRetryResponse = response
			lastRetryAlias = alias
			continue
		}
		finalBackendAlias = alias
		return finalizeProxyResponse(response, requestID, alias, commitState, config.ViaName)
	}
	if lastRetryResponse != nil {
		finalBackendAlias = lastRetryAlias
		return finalizeProxyResponse(lastRetryResponse, requestID, lastRetryAlias, commitState, config.ViaName)
	}
	return proxyFailureResponse(lastFailure, requestID, commitState, config.ViaName)
}

func recordProxyAttemptFailure(observability *observability, requestID, routeAlias, backendAlias string, attempt int, err error, duration time.Duration) {
	if observability == nil {
		return
	}
	reason := "internal_error"
	var failure *proxyError
	if errors.As(err, &failure) {
		reason = string(failure.Kind)
		observability.metrics.recordFailure(failure.Kind)
	}
	observability.publish(decisionEvent{
		Type:           eventAttemptFailed,
		RequestID:      requestID,
		RouteAlias:     routeAlias,
		BackendAlias:   backendAlias,
		Reason:         reason,
		Attempt:        attempt,
		DurationMicros: max(duration.Microseconds(), 0),
	})
}

func recordProxyRetry(observability *observability, requestID, routeAlias, backendAlias string, attempt int, reason string) {
	if observability == nil {
		return
	}
	observability.metrics.recordRetry()
	observability.publish(decisionEvent{Type: eventRetryScheduled, RequestID: requestID, RouteAlias: routeAlias, BackendAlias: backendAlias, Reason: reason, Attempt: attempt + 1})
}

func finalizeProxyResponse(response *httpResponse, requestID, alias string, commitState *responseCommitState, proxyName string) *httpResponse {
	response.CommitState = commitState
	response.Headers = replaceHeader(response.Headers, requestIDHeader, requestID)
	response.Headers = append(response.Headers, headerField{Name: "Proxy-Status", Value: successfulProxyStatus(proxyName, alias, response.StatusCode)})
	return response
}

func passiveOutcomeForError(err error) passiveOutcome {
	var failure *proxyError
	if errors.As(err, &failure) && failure.Kind == proxyCanceled {
		return passiveNeutral
	}
	return passiveFailure
}

func executeProxyAttempt(ctx context.Context, request *httpRequest, backend *proxyBackend, requestID string, config proxyConfig) (*httpResponse, error) {
	connection, _, err := backend.acquireConnection(ctx, config)
	if err != nil {
		kind := proxyDialFailure
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			kind = proxyDialTimeout
		}
		return nil, &proxyError{Kind: kind, BackendAlias: backend.config.Alias, Err: err}
	}
	reusable := false
	defer func() {
		if !reusable {
			_ = connection.Close()
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer func() {
		if !stopCancellation() {
			_ = connection.Close()
		}
	}()

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
		canReuse := response.KeepAlive && response.BodyMode != bodyModeCloseDelimited
		sanitized := sanitizeUpstreamResponse(response, config.ViaName)
		if canReuse {
			reusable = backend.recycleConnection(connection, config.Now())
		}
		return sanitized, nil
	}
	return nil, &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: backend.config.Alias, Err: fmt.Errorf("missing final response")}
}

func canRetryProxyRequest(request *httpRequest, attempt, maxAttempts int, commitState *responseCommitState, now, deadline time.Time) bool {
	if request == nil || attempt >= maxAttempts || commitState.Committed() || !now.Before(deadline) {
		return false
	}
	return request.Method == "GET" || request.Method == "HEAD"
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
		KeepAlive: true,
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
		kind = proxyWriteTimeout
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
	if protocolKind(err) == protocolIncompleteMessage {
		return &proxyError{Kind: proxyUpstreamIncomplete, BackendAlias: alias, Err: err}
	}
	return &proxyError{Kind: proxyUpstreamProtocol, BackendAlias: alias, Err: err}
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func proxyFailureResponse(err error, requestID string, commitState *responseCommitState, proxyName ...string) *httpResponse {
	status := 502
	message := "bad gateway\n"
	viaName := defaultViaName
	if len(proxyName) != 0 && proxyName[0] != "" {
		viaName = proxyName[0]
	}
	var failure *proxyError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case proxyNoBackend, proxyAdmissionRejected, proxyCanceled:
			status, message = 503, "service unavailable\n"
		case proxyDialTimeout, proxyWriteTimeout, proxyUpstreamTimeout:
			status, message = 504, "gateway timeout\n"
		}
	}
	response := textResponse(status, message)
	response.Headers = append(response.Headers, headerField{Name: requestIDHeader, Value: requestID})
	response.Headers = append(response.Headers, headerField{Name: "Proxy-Status", Value: failureProxyStatus(viaName, failure)})
	response.CommitState = commitState
	return response
}

func successfulProxyStatus(proxyName, alias string, status int) string {
	return fmt.Sprintf("%s; received-status=%d; next-hop=%s", proxyName, status, alias)
}

func failureProxyStatus(proxyName string, failure *proxyError) string {
	errorToken := "proxy_internal_error"
	alias := ""
	if failure != nil {
		alias = failure.BackendAlias
		switch failure.Kind {
		case proxyNoBackend:
			errorToken = "destination_unavailable"
		case proxyAdmissionRejected:
			errorToken = "connection_limit_reached"
		case proxyDialFailure:
			errorToken = "connection_refused"
		case proxyDialTimeout:
			errorToken = "connection_timeout"
		case proxyWriteFailure:
			errorToken = "connection_terminated"
		case proxyWriteTimeout:
			errorToken = "connection_write_timeout"
		case proxyUpstreamTimeout:
			errorToken = "connection_read_timeout"
		case proxyUpstreamIncomplete:
			errorToken = "http_response_incomplete"
		case proxyUpstreamProtocol:
			errorToken = "http_protocol_error"
		case proxyCanceled:
			errorToken = "proxy_internal_response"
		}
	}
	value := proxyName + "; error=" + errorToken
	if alias != "" {
		value += "; next-hop=" + alias
	}
	return value
}
