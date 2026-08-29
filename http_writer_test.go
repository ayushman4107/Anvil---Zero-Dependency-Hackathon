package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteHTTPResponseFixed(t *testing.T) {
	response := &httpResponse{
		StatusCode: 200,
		Headers: headerFields{
			{Name: "Content-Type", Value: "text/plain"},
			{Name: "Content-Length", Value: "999"},
		},
		Body: []byte("hello"),
	}
	var output bytes.Buffer
	if err := writeHTTPResponse(&output, response, "GET"); err != nil {
		t.Fatalf("writeHTTPResponse() = %v", err)
	}
	want := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello"
	if output.String() != want {
		t.Fatalf("response = %q\nwant     = %q", output.String(), want)
	}
}

func TestWriteHTTPResponseChunked(t *testing.T) {
	response := &httpResponse{
		StatusCode: 200,
		BodyMode:   bodyModeChunked,
		Body:       []byte("Wikipedia"),
		Trailers:   headerFields{{Name: "X-End", Value: "yes"}},
	}
	var output bytes.Buffer
	if err := writeHTTPResponse(&output, response, "GET"); err != nil {
		t.Fatalf("writeHTTPResponse() = %v", err)
	}
	want := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n9\r\nWikipedia\r\n0\r\nX-End: yes\r\n\r\n"
	if output.String() != want {
		t.Fatalf("response = %q\nwant     = %q", output.String(), want)
	}

	parsed, err := readHTTPResponse(bufio.NewReader(bytes.NewReader(output.Bytes())), defaultHTTPLimits(), "GET")
	if err != nil {
		t.Fatalf("round-trip parse = %v", err)
	}
	if string(parsed.Body) != "Wikipedia" || parsed.BodyMode != bodyModeChunked {
		t.Fatalf("round-trip response = %+v", parsed)
	}
}

func TestWriteHTTPResponseSuppressesForbiddenBodies(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		statusCode int
		wantLength bool
	}{
		{name: "HEAD", method: "HEAD", statusCode: 200, wantLength: true},
		{name: "informational", method: "GET", statusCode: 103},
		{name: "204", method: "GET", statusCode: 204},
		{name: "304", method: "GET", statusCode: 304, wantLength: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			response := &httpResponse{StatusCode: test.statusCode, Body: []byte("forbidden")}
			if err := writeHTTPResponse(&output, response, test.method); err != nil {
				t.Fatalf("writeHTTPResponse() = %v", err)
			}
			parts := strings.SplitN(output.String(), "\r\n\r\n", 2)
			if len(parts) != 2 || parts[1] != "" {
				t.Fatalf("response contains body bytes: %q", output.String())
			}
			hasLength := strings.Contains(parts[0], "Content-Length: 9")
			if hasLength != test.wantLength {
				t.Fatalf("Content-Length presence = %v, want %v: %q", hasLength, test.wantLength, output.String())
			}
		})
	}
}

func TestWriteHTTPResponseRejectsHeaderInjectionBeforeWriting(t *testing.T) {
	response := &httpResponse{
		StatusCode: 200,
		Headers:    headerFields{{Name: "X-Test", Value: "safe\r\nInjected: yes"}},
		Body:       []byte("body"),
	}
	var output bytes.Buffer
	err := writeHTTPResponse(&output, response, "GET")
	if protocolKind(err) != protocolInvalidGeneratedMessage {
		t.Fatalf("error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("writer committed bytes before validation: %q", output.String())
	}
}

func TestWriteHTTPResponseRejectsProtocolUpgrade(t *testing.T) {
	response := &httpResponse{
		StatusCode: 101,
		Headers: headerFields{
			{Name: "Connection", Value: "upgrade"},
			{Name: "Upgrade", Value: "websocket"},
		},
	}
	var output bytes.Buffer
	if err := writeHTTPResponse(&output, response, "GET"); protocolKind(err) != protocolUnsupportedFeature {
		t.Fatalf("error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("writer committed an upgrade response: %q", output.String())
	}
}

func TestWriteHTTPRequestFixedAndChunked(t *testing.T) {
	tests := []struct {
		name string
		req  *httpRequest
		want string
	}{
		{
			name: "fixed",
			req: &httpRequest{
				Method: "POST", Target: "/items", Version: httpVersion11,
				Headers: headerFields{{Name: "Host", Value: "example.test"}},
				Body:    []byte("data"),
			},
			want: "POST /items HTTP/1.1\r\nHost: example.test\r\nContent-Length: 4\r\n\r\ndata",
		},
		{
			name: "chunked",
			req: &httpRequest{
				Method: "POST", Target: "/items", Version: httpVersion11,
				Headers:  headerFields{{Name: "Host", Value: "example.test"}},
				BodyMode: bodyModeChunked,
				Body:     []byte("data"),
				Trailers: headerFields{{Name: "X-End", Value: "yes"}},
			},
			want: "POST /items HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n4\r\ndata\r\n0\r\nX-End: yes\r\n\r\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeHTTPRequest(&output, test.req); err != nil {
				t.Fatalf("writeHTTPRequest() = %v", err)
			}
			if output.String() != test.want {
				t.Fatalf("request = %q\nwant    = %q", output.String(), test.want)
			}
		})
	}
}

func TestChunkedBodyWriterMultipleWritesAndClose(t *testing.T) {
	var output bytes.Buffer
	writer, err := newChunkedBodyWriter(&output, nil)
	if err != nil {
		t.Fatalf("newChunkedBodyWriter() = %v", err)
	}
	if _, err := writer.Write([]byte("ab")); err != nil {
		t.Fatalf("first Write() = %v", err)
	}
	if _, err := writer.Write(nil); err != nil {
		t.Fatalf("empty Write() = %v", err)
	}
	if _, err := writer.Write([]byte("c")); err != nil {
		t.Fatalf("second Write() = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if output.String() != "2\r\nab\r\n1\r\nc\r\n0\r\n\r\n" {
		t.Fatalf("chunk stream = %q", output.String())
	}
	if _, err := writer.Write([]byte("late")); protocolKind(err) != protocolInvalidGeneratedMessage {
		t.Fatalf("write after close error = %v", err)
	}
}

func TestWritersHandleShortWrites(t *testing.T) {
	response := &httpResponse{StatusCode: 200, Body: []byte("hello")}
	short := &shortWriter{maximum: 2}
	if err := writeHTTPResponse(short, response, "GET"); err != nil {
		t.Fatalf("writeHTTPResponse() = %v", err)
	}
	if !strings.HasSuffix(short.output.String(), "\r\n\r\nhello") {
		t.Fatalf("short-writer output = %q", short.output.String())
	}
}

func TestWriterPropagatesFailure(t *testing.T) {
	wantErr := errors.New("write failed")
	response := &httpResponse{StatusCode: 200, Body: []byte("hello")}
	err := writeHTTPResponse(errorWriter{err: wantErr}, response, "GET")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

type shortWriter struct {
	maximum int
	output  bytes.Buffer
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > w.maximum {
		data = data[:w.maximum]
	}
	return w.output.Write(data)
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

var _ io.Writer = (*shortWriter)(nil)
