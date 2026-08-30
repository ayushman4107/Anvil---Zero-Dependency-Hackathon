package main

import (
	"bufio"
	"bytes"
	"io"
	stdhttp "net/http"
	"testing"
)

func TestHTTPRequestWriterStandardLibraryCompatibility(t *testing.T) {
	request := &httpRequest{
		Method:  "POST",
		Target:  "/items?source=anvil",
		Version: httpVersion11,
		Headers: headerFields{
			{Name: "Host", Value: "example.test"},
			{Name: "Content-Type", Value: "text/plain"},
		},
		Body: []byte("payload"),
	}
	var wire bytes.Buffer
	if err := writeHTTPRequest(&wire, request); err != nil {
		t.Fatalf("writeHTTPRequest() = %v", err)
	}

	parsed, err := stdhttp.ReadRequest(bufio.NewReader(bytes.NewReader(wire.Bytes())))
	if err != nil {
		t.Fatalf("net/http.ReadRequest() = %v; wire = %q", err, wire.String())
	}
	defer parsed.Body.Close()
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() = %v", err)
	}
	if parsed.Method != "POST" || parsed.URL.RequestURI() != "/items?source=anvil" || parsed.Host != "example.test" || string(body) != "payload" {
		t.Fatalf("standard-library request = method %q URI %q Host %q body %q", parsed.Method, parsed.URL.RequestURI(), parsed.Host, body)
	}
}

func TestHTTPResponseWriterStandardLibraryCompatibility(t *testing.T) {
	response := &httpResponse{
		StatusCode: 200,
		Headers:    headerFields{{Name: "Content-Type", Value: "text/plain"}},
		BodyMode:   bodyModeChunked,
		Body:       []byte("payload"),
		Trailers:   headerFields{{Name: "X-Anvil-End", Value: "yes"}},
	}
	var wire bytes.Buffer
	if err := writeHTTPResponse(&wire, response, "GET"); err != nil {
		t.Fatalf("writeHTTPResponse() = %v", err)
	}

	request := &stdhttp.Request{Method: "GET"}
	parsed, err := stdhttp.ReadResponse(bufio.NewReader(bytes.NewReader(wire.Bytes())), request)
	if err != nil {
		t.Fatalf("net/http.ReadResponse() = %v; wire = %q", err, wire.String())
	}
	defer parsed.Body.Close()
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() = %v", err)
	}
	if parsed.StatusCode != 200 || string(body) != "payload" || parsed.Trailer.Get("X-Anvil-End") != "yes" {
		t.Fatalf("standard-library response = status %d body %q trailers %v", parsed.StatusCode, body, parsed.Trailer)
	}
}
