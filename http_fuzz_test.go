package main

import (
	"bufio"
	"bytes"
	"testing"
)

func FuzzReadHTTPRequest(f *testing.F) {
	seeds := [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n"),
		[]byte("POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\n\r\ndata"),
		[]byte("POST / HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n0\r\n\r\n"),
		[]byte("GET / HTTP/1.1\nHost: test\n\n"),
		[]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			t.Skip()
		}
		limits := fuzzHTTPLimits()
		_, _ = readHTTPRequest(bufio.NewReaderSize(bytes.NewReader(data), 16), limits)
	})
}

func FuzzReadHTTPResponse(f *testing.F) {
	seeds := [][]byte{
		[]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"),
		[]byte("HTTP/1.1 204 No Content\r\n\r\n"),
		[]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"),
		[]byte("HTTP/1.1 2O0 Broken\r\n\r\n"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			t.Skip()
		}
		limits := fuzzHTTPLimits()
		_, _ = readHTTPResponse(bufio.NewReaderSize(bytes.NewReader(data), 16), limits, "GET")
	})
}

func FuzzParseChunkSizeLine(f *testing.F) {
	for _, seed := range [][]byte{[]byte("0"), []byte("a"), []byte("10;name=value"), []byte("fffffffffffffffff"), []byte("1;name=\"quoted\"")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > 4*1024 {
			t.Skip()
		}
		_, _ = parseChunkSizeLine(line)
	})
}

func fuzzHTTPLimits() httpLimits {
	limits := defaultHTTPLimits()
	limits.MaxStartLineBytes = 4 * 1024
	limits.MaxHeaderBytes = 8 * 1024
	limits.MaxHeaderFields = 64
	limits.MaxBodyBytes = 8 * 1024
	limits.MaxChunkLineBytes = 1024
	limits.MaxChunkCount = 1024
	limits.MaxTrailerBytes = 2 * 1024
	limits.MaxTrailerFields = 32
	return limits
}
