package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPServerStandardLibraryClientInteropAndReuse(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/hello/:name", func(_ context.Context, _ *httpRequest, params routeParams) (*httpResponse, error) {
		name, _ := params.Get("name")
		return textResponse(200, "hello "+name), nil
	})
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()

	var dials atomic.Int64
	transport := &stdhttp.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	client := &stdhttp.Client{Transport: transport, Timeout: 3 * time.Second}
	defer transport.CloseIdleConnections()
	for _, name := range []string{"Ada", "Grace"} {
		response, err := client.Get("http://" + address + "/hello/" + name)
		if err != nil {
			t.Fatalf("GET /hello/%s: %v", name, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || string(body) != "hello "+name {
			t.Fatalf("response = status %d body %q read error %v", response.StatusCode, body, readErr)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("client dial count = %d, want one reused HTTP/1.1 connection", dials.Load())
	}
}

func TestHTTPServerSequentialBufferedRequests(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/one", namedHandler("one"))
	mustRegisterRoute(t, router, "GET", "/two", namedHandler("two"))
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()

	connection := dialHTTPTestServer(t, address)
	defer connection.Close()
	raw := "GET /one HTTP/1.1\r\nHost: test\r\n\r\nGET /two HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(connection, raw); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	first, err := readHTTPResponse(reader, defaultHTTPLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	second, err := readHTTPResponse(reader, defaultHTTPLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Body) != "one" || string(second.Body) != "two" || second.KeepAlive {
		t.Fatalf("responses = first %q, second %q keep-alive %v", first.Body, second.Body, second.KeepAlive)
	}
	assertConnectionClosed(t, connection, reader)
}

func TestHTTPServerRequestFragmentedOneByteAtATime(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "POST", "/echo", func(_ context.Context, request *httpRequest, _ routeParams) (*httpResponse, error) {
		return textResponse(200, string(request.Body)), nil
	})
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()

	connection := dialHTTPTestServer(t, address)
	defer connection.Close()
	raw := []byte("POST /echo HTTP/1.1\r\nHost: test\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello")
	for _, octet := range raw {
		if _, err := connection.Write([]byte{octet}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := readHTTPResponse(bufio.NewReader(connection), defaultHTTPLimits(), "POST")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(response.Body) != "hello" {
		t.Fatalf("fragmented response = status %d body %q", response.StatusCode, response.Body)
	}
}

func TestHTTPServerNotFoundAndMethodNotAllowed(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "POST", "/items", namedHandler("post"))
	mustRegisterRoute(t, router, "GET", "/items", namedHandler("get"))
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()

	response := rawHTTPExchange(t, address, "DELETE /items HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "DELETE")
	if response.StatusCode != 405 {
		t.Fatalf("DELETE /items status = %d, want 405", response.StatusCode)
	}
	if allow, _ := response.Headers.First("Allow"); allow != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", allow)
	}
	response = rawHTTPExchange(t, address, "GET /missing HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 404 {
		t.Fatalf("GET /missing status = %d, want 404", response.StatusCode)
	}
}

func TestHTTPServerProtocolErrorMappingsCloseConnection(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "POST", "/upload", namedHandler("uploaded"))
	config := testHTTPConfig()
	config.Limits.MaxBodyBytes = 4
	address, _, stop := startHTTPTestServer(t, router, config)
	defer stop()

	tests := []struct {
		name   string
		raw    string
		method string
		status int
	}{
		{name: "malformed", raw: "GET / HTTP/1.1\r\n\r\n", method: "GET", status: 400},
		{name: "body too large", raw: "POST /upload HTTP/1.1\r\nHost: test\r\nContent-Length: 5\r\n\r\n", method: "POST", status: 413},
		{name: "unsupported coding", raw: "POST /upload HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: gzip, chunked\r\n\r\n", method: "POST", status: 501},
		{name: "expect", raw: "POST /upload HTTP/1.1\r\nHost: test\r\nExpect: 100-continue\r\nContent-Length: 0\r\n\r\n", method: "POST", status: 417},
		{name: "upgrade", raw: "GET /upload HTTP/1.1\r\nHost: test\r\nConnection: upgrade\r\nUpgrade: websocket\r\n\r\n", method: "GET", status: 400},
		{name: "connect", raw: "CONNECT /upload HTTP/1.1\r\nHost: test\r\n\r\n", method: "CONNECT", status: 400},
		{name: "HTTP2 preface", raw: "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", method: "PRI", status: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := dialHTTPTestServer(t, address)
			defer connection.Close()
			if _, err := io.WriteString(connection, test.raw); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(connection)
			response, err := readHTTPResponse(reader, defaultHTTPLimits(), test.method)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status || response.KeepAlive {
				t.Fatalf("status = %d, keep-alive = %v; want %d, false", response.StatusCode, response.KeepAlive, test.status)
			}
			assertConnectionClosed(t, connection, reader)
		})
	}
}

func TestHTTPServerHandlerPanicAndFailureAreContained(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/panic", func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		panic("test panic")
	})
	mustRegisterRoute(t, router, "GET", "/error", func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		return nil, fmt.Errorf("test failure")
	})
	mustRegisterRoute(t, router, "GET", "/health", namedHandler("ok"))
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()

	for _, path := range []string{"/panic", "/error"} {
		response := rawHTTPExchange(t, address, "GET "+path+" HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
		if response.StatusCode != 500 {
			t.Fatalf("GET %s status = %d, want 500", path, response.StatusCode)
		}
	}
	response := rawHTTPExchange(t, address, "GET /health HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 200 || string(response.Body) != "ok" {
		t.Fatalf("health after failures = status %d body %q", response.StatusCode, response.Body)
	}
}

func TestHTTPServerRequestLimitClosesPredictably(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/health", namedHandler("ok"))
	config := testHTTPConfig()
	config.MaxRequestsPerConnection = 2
	address, _, stop := startHTTPTestServer(t, router, config)
	defer stop()

	connection := dialHTTPTestServer(t, address)
	defer connection.Close()
	if _, err := io.WriteString(connection, strings.Repeat("GET /health HTTP/1.1\r\nHost: test\r\n\r\n", 3)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	first, err := readHTTPResponse(reader, defaultHTTPLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	second, err := readHTTPResponse(reader, defaultHTTPLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	if !first.KeepAlive || second.KeepAlive {
		t.Fatalf("keep-alive sequence = %v, %v; want true, false", first.KeepAlive, second.KeepAlive)
	}
	assertConnectionClosed(t, connection, reader)
}

func TestHTTPServerInvalidGeneratedResponseFallsBackAndCloses(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/unsafe", func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		response := textResponse(200, "unsafe")
		response.Headers = append(response.Headers, headerField{Name: "X-Test", Value: "bad\r\nInjected: yes"})
		return response, nil
	})
	address, _, stop := startHTTPTestServer(t, router, testHTTPConfig())
	defer stop()
	response := rawHTTPExchange(t, address, "GET /unsafe HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", "GET")
	if response.StatusCode != 500 || len(response.Headers.Values("Injected")) != 0 {
		t.Fatalf("fallback = status %d headers %v", response.StatusCode, response.Headers)
	}
}

func testHTTPConfig() httpServerConfig {
	return httpServerConfig{Limits: defaultHTTPLimits(), MaxRequestsPerConnection: 100}
}

func startHTTPTestServer(t *testing.T, router *routeTree, httpConfig httpServerConfig) (string, *tcpServer, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPConnectionHandler(router, httpConfig)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	tcpConfig := tcpServerConfig{
		MaxConnections:  32,
		ReadTimeout:     2 * time.Second,
		WriteTimeout:    2 * time.Second,
		IdleTimeout:     2 * time.Second,
		ShutdownTimeout: 500 * time.Millisecond,
		ForceCloseWait:  500 * time.Millisecond,
	}
	server, err := newTCPServer(listener, tcpConfig, handler)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not shut down")
		}
	}
	return listener.Addr().String(), server, stop
}

func dialHTTPTestServer(t *testing.T, address string) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	return connection
}

func rawHTTPExchange(t *testing.T, address, raw, method string) *httpResponse {
	t.Helper()
	connection := dialHTTPTestServer(t, address)
	defer connection.Close()
	if _, err := io.WriteString(connection, raw); err != nil {
		t.Fatal(err)
	}
	response, err := readHTTPResponse(bufio.NewReader(connection), defaultHTTPLimits(), method)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertConnectionClosed(t *testing.T, connection net.Conn, reader *bufio.Reader) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errorsIsEOF(err) {
		t.Fatalf("read after close = %v, want EOF", err)
	}
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}
