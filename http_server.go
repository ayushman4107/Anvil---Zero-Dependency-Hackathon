package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const defaultMaxRequestsPerConnection = 1_000

type httpServerConfig struct {
	Limits                   httpLimits
	MaxRequestsPerConnection int
}

func (c httpServerConfig) validate() error {
	if err := c.Limits.validate(); err != nil {
		return err
	}
	if c.MaxRequestsPerConnection <= 0 {
		return fmt.Errorf("maximum requests per connection must be greater than zero")
	}
	return nil
}

func newHTTPConnectionHandler(router *routeTree, config httpServerConfig) (connectionHandler, error) {
	if router == nil {
		return nil, fmt.Errorf("route tree is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	if err := router.Freeze(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, connection *clientConn) error {
		return serveHTTPConnection(ctx, connection, router, config)
	}, nil
}

func serveHTTPConnection(ctx context.Context, connection *clientConn, router *routeTree, config httpServerConfig) error {
	if ctx == nil || connection == nil {
		return fmt.Errorf("HTTP connection context and connection are required")
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)

	for requestNumber := 1; requestNumber <= config.MaxRequestsPerConnection; requestNumber++ {
		if err := ctx.Err(); err != nil {
			return nil
		}
		request, err := readHTTPRequest(reader, config.Limits)
		if err != nil {
			if errors.Is(err, io.EOF) || protocolKind(err) == protocolTimeout || ctx.Err() != nil {
				return nil
			}
			response := protocolFailureResponse(err)
			if _, writeErr := writeBufferedHTTPResponse(writer, response, ""); writeErr != nil {
				return writeErr
			}
			return nil
		}
		request.RemoteAddr = connection.RemoteAddr().String()

		response := dispatchHTTPRequest(ctx, router, request)
		closeAfterResponse := !request.KeepAlive || response.Close || response.Headers.HasToken("Connection", "close") || requestNumber == config.MaxRequestsPerConnection
		response.Close = closeAfterResponse
		response.KeepAlive = !closeAfterResponse
		usedFallback, err := writeBufferedHTTPResponse(writer, response, request.Method)
		if err != nil {
			return err
		}
		if closeAfterResponse || usedFallback {
			return nil
		}
	}
	return nil
}

func dispatchHTTPRequest(ctx context.Context, router *routeTree, request *httpRequest) (response *httpResponse) {
	resolution := router.Lookup(request.Method, request.Target)
	if resolution.Handler == nil {
		if len(resolution.AllowedMethods) != 0 {
			response = textResponse(405, "method not allowed\n")
			response.Headers = append(response.Headers, headerField{Name: "Allow", Value: strings.Join(resolution.AllowedMethods, ", ")})
			return response
		}
		return textResponse(404, "route not found\n")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			response = textResponse(500, "internal server error\n")
			response.Close = true
		}
	}()
	response, err := resolution.Handler(ctx, request, resolution.Params)
	if err != nil || response == nil {
		response = textResponse(500, "internal server error\n")
		response.Close = true
	}
	return response
}

func protocolFailureResponse(err error) *httpResponse {
	status := 400
	message := "bad request\n"
	var protocolErr *protocolError
	if errors.As(err, &protocolErr) {
		switch protocolErr.Kind {
		case protocolBodyTooLarge:
			status, message = 413, "request body too large\n"
		case protocolUnsupportedTransferCoding:
			status, message = 501, "transfer coding not implemented\n"
		case protocolUnsupportedFeature:
			if strings.Contains(strings.ToLower(protocolErr.Detail), "expect") {
				status, message = 417, "expectation failed\n"
			}
		}
	}
	response := textResponse(status, message)
	response.Close = true
	return response
}

func textResponse(status int, body string) *httpResponse {
	return &httpResponse{
		Version:    httpVersion11,
		StatusCode: status,
		Headers: headerFields{
			{Name: "Content-Type", Value: "text/plain; charset=utf-8"},
		},
		Body:      []byte(body),
		BodyMode:  bodyModeFixed,
		KeepAlive: true,
	}
}

func writeBufferedHTTPResponse(writer *bufio.Writer, response *httpResponse, requestMethod string) (bool, error) {
	var encoded bytes.Buffer
	usedFallback := false
	commitState := response.CommitState
	if err := writeHTTPResponse(&encoded, response, requestMethod); err != nil {
		usedFallback = true
		fallback := textResponse(500, "internal server error\n")
		fallback.Close = true
		encoded.Reset()
		if fallbackErr := writeHTTPResponse(&encoded, fallback, requestMethod); fallbackErr != nil {
			return usedFallback, fallbackErr
		}
	}
	commitState.MarkCommitted()
	if err := writeAll(writer, encoded.Bytes()); err != nil {
		return usedFallback, err
	}
	return usedFallback, writer.Flush()
}
