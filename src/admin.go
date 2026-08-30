package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAdminListen      = "127.0.0.1:9090"
	defaultSSEHeartbeat     = 5 * time.Second
	defaultAdminConnections = 64
	defaultAdminMaxRequests = 100
)

type adminConfig struct {
	Heartbeat time.Duration
	HTTP      httpServerConfig
	TCP       tcpServerConfig
}

func defaultAdminConfig() adminConfig {
	return adminConfig{
		Heartbeat: defaultSSEHeartbeat,
		HTTP: httpServerConfig{
			Limits:                   defaultHTTPLimits(),
			MaxRequestsPerConnection: defaultAdminMaxRequests,
		},
		TCP: tcpServerConfig{
			MaxConnections:  defaultAdminConnections,
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     30 * time.Second,
			ShutdownTimeout: 3 * time.Second,
			ForceCloseWait:  time.Second,
		},
	}
}

func (c adminConfig) validate() error {
	if c.Heartbeat <= 0 {
		return fmt.Errorf("SSE heartbeat must be greater than zero")
	}
	if err := c.HTTP.validate(); err != nil {
		return err
	}
	if err := c.TCP.validate(); err != nil {
		return err
	}
	if c.TCP.IdleTimeout <= c.Heartbeat {
		return fmt.Errorf("admin idle timeout must exceed the SSE heartbeat interval")
	}
	return nil
}

func validateAdminListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("admin listen address must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("admin listener must use an explicit loopback IP")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65_535 {
		return fmt.Errorf("admin listener port must be between 0 and 65535")
	}
	return nil
}

func newAdminServer(listener net.Listener, config adminConfig, observability *observability) (*tcpServer, error) {
	if listener == nil {
		return nil, fmt.Errorf("admin listener is required")
	}
	if observability == nil {
		return nil, fmt.Errorf("observability is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	handler := func(ctx context.Context, connection *clientConn) error {
		return serveAdminConnection(ctx, connection, observability, config)
	}
	return newTCPServer(listener, config.TCP, handler)
}

func serveAdminConnection(ctx context.Context, connection *clientConn, observability *observability, config adminConfig) error {
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	for requestNumber := 1; requestNumber <= config.HTTP.MaxRequestsPerConnection; requestNumber++ {
		if ctx.Err() != nil {
			return nil
		}
		request, err := readHTTPRequest(reader, config.HTTP.Limits)
		if err != nil {
			if errors.Is(err, io.EOF) || protocolKind(err) == protocolTimeout || ctx.Err() != nil {
				return nil
			}
			response := protocolFailureResponse(err)
			_, writeErr := writeBufferedHTTPResponse(writer, response, "")
			return writeErr
		}
		request.RemoteAddr = connection.RemoteAddr().String()
		path := requestPath(request.Target)
		if path == "/api/events" && request.Method == "GET" {
			return serveSSE(ctx, writer, request, observability, config.Heartbeat)
		}
		response := adminResponse(request, observability)
		closeAfterResponse := !request.KeepAlive || response.Close || requestNumber == config.HTTP.MaxRequestsPerConnection
		response.Close = closeAfterResponse
		response.KeepAlive = !closeAfterResponse
		fallback, writeErr := writeBufferedHTTPResponse(writer, response, request.Method)
		if writeErr != nil {
			return writeErr
		}
		if closeAfterResponse || fallback {
			return nil
		}
	}
	return nil
}

func adminResponse(request *httpRequest, observability *observability) *httpResponse {
	path := requestPath(request.Target)
	if request.Method != "GET" {
		response := textResponse(405, "method not allowed\n")
		response.Headers = append(response.Headers, headerField{Name: "Allow", Value: "GET"})
		return hardenAdminResponse(response)
	}
	switch path {
	case "/", "/index.html":
		return hardenAdminResponse(&httpResponse{
			Version:    httpVersion11,
			StatusCode: 200,
			Headers: headerFields{
				{Name: "Content-Type", Value: "text/html; charset=utf-8"},
				{Name: "Cache-Control", Value: "no-store"},
				{Name: "Content-Security-Policy", Value: "default-src 'none'; connect-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'"},
			},
			Body:      []byte(dashboardHTML),
			BodyMode:  bodyModeFixed,
			KeepAlive: true,
		})
	case "/api/metrics":
		body, err := json.Marshal(observability.snapshot())
		if err != nil {
			return hardenAdminResponse(textResponse(500, "metrics encoding failed\n"))
		}
		return hardenAdminResponse(&httpResponse{
			Version:    httpVersion11,
			StatusCode: 200,
			Headers:    headerFields{{Name: "Content-Type", Value: "application/json; charset=utf-8"}, {Name: "Cache-Control", Value: "no-store"}},
			Body:       body,
			BodyMode:   bodyModeFixed,
			KeepAlive:  true,
		})
	case "/healthz":
		return hardenAdminResponse(textResponse(200, "ok\n"))
	default:
		return hardenAdminResponse(textResponse(404, "route not found\n"))
	}
}

func hardenAdminResponse(response *httpResponse) *httpResponse {
	response.Headers = append(response.Headers,
		headerField{Name: "X-Content-Type-Options", Value: "nosniff"},
		headerField{Name: "Referrer-Policy", Value: "no-referrer"},
	)
	return response
}

func serveSSE(ctx context.Context, writer *bufio.Writer, request *httpRequest, observability *observability, heartbeat time.Duration) error {
	after, err := parseLastEventID(request.Headers)
	if err != nil {
		response := hardenAdminResponse(textResponse(400, "invalid Last-Event-ID\n"))
		response.Close = true
		_, writeErr := writeBufferedHTTPResponse(writer, response, request.Method)
		return writeErr
	}
	subscription, err := observability.hub.subscribe()
	if err != nil {
		response := hardenAdminResponse(textResponse(503, "event subscriber limit reached\n"))
		response.Close = true
		_, writeErr := writeBufferedHTTPResponse(writer, response, request.Method)
		return writeErr
	}
	defer observability.hub.unsubscribe(subscription.ID)
	replay := observability.ledger.snapshotSince(after)
	if err := writeSSEHead(writer); err != nil {
		return err
	}
	chunked, err := newChunkedBodyWriter(writer, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = chunked.Close()
		_ = writer.Flush()
	}()
	if err := writeSSEPayload(chunked, writer, []byte(": anvil stream ready\n\n")); err != nil {
		return err
	}
	if replay.Gap {
		gap, _ := json.Marshal(struct {
			OldestSequence uint64 `json:"oldest_sequence"`
			LatestSequence uint64 `json:"latest_sequence"`
		}{replay.OldestSequence, replay.LatestSequence})
		if err := writeSSEPayload(chunked, writer, append([]byte("event: gap\ndata: "), append(gap, []byte("\n\n")...)...)); err != nil {
			return err
		}
	}
	lastSent := after
	if replay.Gap && after > replay.LatestSequence {
		lastSent = replay.OldestSequence - 1
	}
	for _, event := range replay.Events {
		if event.Sequence <= lastSent {
			continue
		}
		if err := writeSSEEvent(chunked, writer, event); err != nil {
			return err
		}
		lastSent = event.Sequence
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-subscription.Events:
			if event.Sequence <= lastSent {
				continue
			}
			if err := writeSSEEvent(chunked, writer, event); err != nil {
				return err
			}
			lastSent = event.Sequence
		case <-subscription.Done:
			return nil
		case <-ticker.C:
			if err := writeSSEPayload(chunked, writer, []byte(": heartbeat\n\n")); err != nil {
				return err
			}
		}
	}
}

func parseLastEventID(headers headerFields) (uint64, error) {
	values := headers.Values("Last-Event-ID")
	if len(values) == 0 {
		return 0, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("Last-Event-ID must occur once")
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func writeSSEHead(writer *bufio.Writer) error {
	headers := headerFields{
		{Name: "Content-Type", Value: "text/event-stream; charset=utf-8"},
		{Name: "Cache-Control", Value: "no-cache"},
		{Name: "Connection", Value: "close"},
		{Name: "Transfer-Encoding", Value: "chunked"},
		{Name: "X-Accel-Buffering", Value: "no"},
		{Name: "X-Content-Type-Options", Value: "nosniff"},
	}
	if err := validateGeneratedFields(headers, "SSE headers"); err != nil {
		return err
	}
	if err := writeAll(writer, []byte("HTTP/1.1 200 OK\r\n")); err != nil {
		return err
	}
	if err := writeHeaderBlock(writer, headers); err != nil {
		return err
	}
	return writer.Flush()
}

func writeSSEEvent(chunked *chunkedBodyWriter, writer *bufio.Writer, event decisionEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data)
	return writeSSEPayload(chunked, writer, []byte(payload))
}

func writeSSEPayload(chunked *chunkedBodyWriter, writer *bufio.Writer, payload []byte) error {
	if _, err := chunked.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}
