package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestReadHTTPRequestMinimal(t *testing.T) {
	request := mustReadRequest(t, "GET /health?full=1 HTTP/1.1\r\nHost: example.test\r\nX-Trace: first\r\nX-Trace: second\r\n\r\n")
	if request.Method != "GET" || request.Target != "/health?full=1" || request.Version != httpVersion11 {
		t.Fatalf("unexpected request line: %+v", request)
	}
	if request.BodyMode != bodyModeNone || len(request.Body) != 0 || !request.KeepAlive {
		t.Fatalf("unexpected request framing: %+v", request)
	}
	if got := request.Headers.Values("x-trace"); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("ordered duplicate values = %v", got)
	}
}

func TestReadHTTPRequestFragmentationIndependent(t *testing.T) {
	raw := []byte("POST /items HTTP/1.1\r\nHost: example.test\r\nContent-Length: 11\r\nX-Test: value\r\n\r\nhello world")
	want, err := readHTTPRequest(bufio.NewReader(bytes.NewReader(raw)), defaultHTTPLimits())
	if err != nil {
		t.Fatalf("contiguous parse = %v", err)
	}

	patterns := [][]int{{1}, {2, 1, 3, 1, 5}, {7, 4, 2, 9, 1, 1, 8}}
	for _, pattern := range patterns {
		reader := bufio.NewReaderSize(&fragmentReader{data: raw, pattern: pattern}, 16)
		got, err := readHTTPRequest(reader, defaultHTTPLimits())
		if err != nil {
			t.Fatalf("pattern %v parse = %v", pattern, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pattern %v result differs\n got: %#v\nwant: %#v", pattern, got, want)
		}
	}
}

func TestReadHTTPRequestPreservesBufferedNextMessage(t *testing.T) {
	raw := "POST /one HTTP/1.1\r\nHost: test\r\nContent-Length: 3\r\n\r\none" +
		"GET /two HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"
	reader := bufio.NewReader(strings.NewReader(raw))
	first, err := readHTTPRequest(reader, defaultHTTPLimits())
	if err != nil {
		t.Fatalf("first request = %v", err)
	}
	second, err := readHTTPRequest(reader, defaultHTTPLimits())
	if err != nil {
		t.Fatalf("second request = %v", err)
	}
	if string(first.Body) != "one" || second.Target != "/two" || second.KeepAlive {
		t.Fatalf("unexpected sequential requests: first=%+v second=%+v", first, second)
	}
}

func TestReadHTTPRequestFixedBodyLeavesExtraBytes(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\n\r\ndataNEXT"))
	request, err := readHTTPRequest(reader, defaultHTTPLimits())
	if err != nil {
		t.Fatalf("readHTTPRequest() = %v", err)
	}
	if string(request.Body) != "data" {
		t.Fatalf("body = %q", request.Body)
	}
	extra := make([]byte, 4)
	if _, err := io.ReadFull(reader, extra); err != nil || string(extra) != "NEXT" {
		t.Fatalf("extra bytes = %q, err = %v", extra, err)
	}
}

func TestReadHTTPRequestChunkedWithTrailers(t *testing.T) {
	raw := "POST /upload HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n\r\n" +
		"4;kind=first\r\nWiki\r\n5; note=\"two words\"\r\npedia\r\n0\r\nX-Checksum: yes\r\n\r\n"
	request := mustReadRequest(t, raw)
	if request.BodyMode != bodyModeChunked || string(request.Body) != "Wikipedia" {
		t.Fatalf("unexpected chunked request: %+v", request)
	}
	if value, ok := request.Trailers.First("x-checksum"); !ok || value != "yes" {
		t.Fatalf("trailers = %+v", request.Trailers)
	}
}

func TestReadHTTPRequestFragmentedChunkDelimiters(t *testing.T) {
	raw := []byte(chunkedRequest("2\r\nab\r\n3\r\ncde\r\n0\r\n\r\n"))
	reader := bufio.NewReaderSize(&fragmentReader{data: raw, pattern: []int{1}}, 16)
	request, err := readHTTPRequest(reader, defaultHTTPLimits())
	if err != nil {
		t.Fatalf("readHTTPRequest() = %v", err)
	}
	if string(request.Body) != "abcde" {
		t.Fatalf("body = %q", request.Body)
	}
}

func TestReadHTTPRequestIdenticalContentLengths(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\nContent-Length: 4, 4\r\n\r\ndata"
	request := mustReadRequest(t, raw)
	if request.BodyMode != bodyModeFixed || string(request.Body) != "data" {
		t.Fatalf("unexpected fixed request: %+v", request)
	}
}

func TestReadHTTPRequestValidHostAuthorities(t *testing.T) {
	for _, host := range []string{"example.test", "example.test:8080", "127.0.0.1:80", "[::1]", "[2001:db8::1]:443"} {
		t.Run(host, func(t *testing.T) {
			request := mustReadRequest(t, "GET / HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
			if got, _ := request.Headers.First("Host"); got != host {
				t.Fatalf("Host = %q, want %q", got, host)
			}
		})
	}
}

func TestReadHTTPRequestErrors(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		limits func() httpLimits
		kind   protocolErrorKind
	}{
		{name: "partial start line", raw: "GET / HTTP/1.1", kind: protocolIncompleteMessage},
		{name: "bare LF start line", raw: "GET / HTTP/1.1\nHost: test\r\n\r\n", kind: protocolMalformedStartLine},
		{name: "extra request-line space", raw: "GET  / HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolMalformedStartLine},
		{name: "invalid method", raw: "GE(T / HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolMalformedStartLine},
		{name: "unsupported version", raw: "GET / HTTP/1.0\r\nHost: test\r\n\r\n", kind: protocolUnsupportedVersion},
		{name: "HTTP2 preface", raw: "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", kind: protocolUnsupportedVersion},
		{name: "absolute target", raw: "GET http://test/ HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolUnsupportedTarget},
		{name: "fragment in target", raw: "GET /path#fragment HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolUnsupportedTarget},
		{name: "invalid target escape", raw: "GET /bad%Q0 HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolUnsupportedTarget},
		{name: "asterisk wrong method", raw: "GET * HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolUnsupportedTarget},
		{name: "CONNECT", raw: "CONNECT / HTTP/1.1\r\nHost: test\r\n\r\n", kind: protocolUnsupportedTarget},
		{name: "missing Host", raw: "GET / HTTP/1.1\r\nUser-Agent: test\r\n\r\n", kind: protocolMissingHost},
		{name: "duplicate Host", raw: "GET / HTTP/1.1\r\nHost: one\r\nHost: two\r\n\r\n", kind: protocolMalformedHeader},
		{name: "invalid Host port", raw: "GET / HTTP/1.1\r\nHost: test:not-a-port\r\n\r\n", kind: protocolMalformedHeader},
		{name: "unbracketed IPv6 Host", raw: "GET / HTTP/1.1\r\nHost: ::1\r\n\r\n", kind: protocolMalformedHeader},
		{name: "broken IPv6 Host", raw: "GET / HTTP/1.1\r\nHost: [::1\r\n\r\n", kind: protocolMalformedHeader},
		{name: "whitespace before colon", raw: "GET / HTTP/1.1\r\nHost : test\r\n\r\n", kind: protocolMalformedHeader},
		{name: "obs fold", raw: "GET / HTTP/1.1\r\nHost: test\r\n folded\r\n\r\n", kind: protocolMalformedHeader},
		{name: "bare LF header", raw: "GET / HTTP/1.1\r\nHost: test\n\n", kind: protocolMalformedHeader},
		{name: "embedded CR", raw: "GET / HTTP/1.1\r\nHost: te\rst\r\n\r\n", kind: protocolMalformedHeader},
		{name: "bad Connection token", raw: "GET / HTTP/1.1\r\nHost: test\r\nConnection: close, bad token\r\n\r\n", kind: protocolMalformedHeader},
		{name: "upgrade", raw: "GET / HTTP/1.1\r\nHost: test\r\nConnection: upgrade\r\nUpgrade: websocket\r\n\r\n", kind: protocolUnsupportedFeature},
		{name: "expect", raw: "POST / HTTP/1.1\r\nHost: test\r\nExpect: 100-continue\r\nContent-Length: 0\r\n\r\n", kind: protocolUnsupportedFeature},
		{name: "TE and CL", raw: "POST / HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\nContent-Length: 0\r\n\r\n", kind: protocolAmbiguousFraming},
		{name: "conflicting CL", raw: "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 2, 3\r\n\r\nabc", kind: protocolAmbiguousFraming},
		{name: "signed CL", raw: "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: +1\r\n\r\nx", kind: protocolInvalidContentLength},
		{name: "overflow CL", raw: "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 999999999999999999999\r\n\r\n", kind: protocolInvalidContentLength},
		{name: "unsupported coding", raw: "POST / HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: gzip, chunked\r\n\r\n", kind: protocolUnsupportedTransferCoding},
		{name: "premature fixed body", raw: "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\n\r\nab", kind: protocolIncompleteMessage},
		{name: "body limit", raw: "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\n\r\ndata", limits: bodyLimit(3), kind: protocolBodyTooLarge},
		{name: "invalid chunk digit", raw: chunkedRequest("Z\r\n"), kind: protocolInvalidChunk},
		{name: "overflow chunk", raw: chunkedRequest("fffffffffffffffff\r\n"), kind: protocolInvalidChunk},
		{name: "invalid chunk extension", raw: chunkedRequest("1;=bad\r\nx\r\n0\r\n\r\n"), kind: protocolInvalidChunk},
		{name: "missing chunk CRLF", raw: chunkedRequest("1\r\nxX0\r\n\r\n"), kind: protocolInvalidChunk},
		{name: "forbidden trailer", raw: chunkedRequest("0\r\nContent-Length: 0\r\n\r\n"), kind: protocolMalformedHeader},
		{name: "decoded chunk limit", raw: chunkedRequest("4\r\ndata\r\n0\r\n\r\n"), limits: bodyLimit(3), kind: protocolBodyTooLarge},
		{name: "chunk count limit", raw: chunkedRequest("1\r\na\r\n1\r\nb\r\n0\r\n\r\n"), limits: chunkCountLimit(2), kind: protocolLimitExceeded},
		{name: "start-line limit", raw: "GET /long HTTP/1.1\r\nHost: test\r\n\r\n", limits: startLineLimit(8), kind: protocolLimitExceeded},
		{name: "header byte limit", raw: "GET / HTTP/1.1\r\nHost: test\r\n\r\n", limits: headerByteLimit(8), kind: protocolLimitExceeded},
		{name: "header count limit", raw: "GET / HTTP/1.1\r\nHost: test\r\nX: 1\r\n\r\n", limits: headerCountLimit(1), kind: protocolLimitExceeded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := defaultHTTPLimits()
			if test.limits != nil {
				limits = test.limits()
			}
			_, err := readHTTPRequest(bufio.NewReaderSize(strings.NewReader(test.raw), 16), limits)
			if err == nil {
				t.Fatal("readHTTPRequest() = nil error")
			}
			if got := protocolKind(err); got != test.kind {
				t.Fatalf("error kind = %q, want %q; err = %v", got, test.kind, err)
			}
		})
	}
}

func TestReadHTTPResponseFraming(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		raw         string
		wantMode    bodyMode
		wantBody    string
		wantTrailer string
		keepAlive   bool
	}{
		{name: "fixed", method: "GET", raw: "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\ndata", wantMode: bodyModeFixed, wantBody: "data", keepAlive: true},
		{name: "chunked", method: "GET", raw: "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\nX-End: yes\r\n\r\n", wantMode: bodyModeChunked, wantBody: "abc", wantTrailer: "yes", keepAlive: true},
		{name: "close delimited", method: "GET", raw: "HTTP/1.1 200 OK\r\n\r\nbody", wantMode: bodyModeCloseDelimited, wantBody: "body", keepAlive: false},
		{name: "HEAD", method: "HEAD", raw: "HTTP/1.1 200 OK\r\nContent-Length: 99\r\n\r\n", wantMode: bodyModeNone, keepAlive: true},
		{name: "204", method: "GET", raw: "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n", wantMode: bodyModeNone, keepAlive: true},
		{name: "304", method: "GET", raw: "HTTP/1.1 304 Not Modified\r\nContent-Length: 10\r\n\r\n", wantMode: bodyModeNone, keepAlive: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := readHTTPResponse(bufio.NewReader(strings.NewReader(test.raw)), defaultHTTPLimits(), test.method)
			if err != nil {
				t.Fatalf("readHTTPResponse() = %v", err)
			}
			if response.BodyMode != test.wantMode || string(response.Body) != test.wantBody || response.KeepAlive != test.keepAlive {
				t.Fatalf("unexpected response: %+v", response)
			}
			if test.wantTrailer != "" {
				if value, _ := response.Trailers.First("X-End"); value != test.wantTrailer {
					t.Fatalf("trailer = %q", value)
				}
			}
		})
	}
}

func TestReadHTTPResponseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind protocolErrorKind
	}{
		{name: "bad status digits", raw: "HTTP/1.1 2O0 OK\r\nContent-Length: 0\r\n\r\n", kind: protocolMalformedStartLine},
		{name: "bad status range", raw: "HTTP/1.1 700 Nope\r\nContent-Length: 0\r\n\r\n", kind: protocolMalformedStartLine},
		{name: "ambiguous response", raw: "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n", kind: protocolAmbiguousFraming},
		{name: "switching protocols", raw: "HTTP/1.1 101 Switching Protocols\r\nConnection: upgrade\r\nUpgrade: websocket\r\n\r\n", kind: protocolUnsupportedFeature},
		{name: "incomplete fixed response", raw: "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\nx", kind: protocolIncompleteMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readHTTPResponse(bufio.NewReader(strings.NewReader(test.raw)), defaultHTTPLimits(), "GET")
			if got := protocolKind(err); got != test.kind {
				t.Fatalf("error kind = %q, want %q; err = %v", got, test.kind, err)
			}
		})
	}
}

func TestReadHTTPRequestCleanEOF(t *testing.T) {
	_, err := readHTTPRequest(bufio.NewReader(strings.NewReader("")), defaultHTTPLimits())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream error = %v, want io.EOF", err)
	}
}

func TestReadHTTPResponseAllowsRejectedCONNECTButNotTunnel(t *testing.T) {
	rejected := "HTTP/1.1 400 Bad Request\r\nContent-Length: 3\r\nConnection: close\r\n\r\nbad"
	response, err := readHTTPResponse(bufio.NewReader(strings.NewReader(rejected)), defaultHTTPLimits(), "CONNECT")
	if err != nil {
		t.Fatalf("rejected CONNECT response: %v", err)
	}
	if response.StatusCode != 400 || string(response.Body) != "bad" {
		t.Fatalf("rejected CONNECT response = status %d body %q", response.StatusCode, response.Body)
	}

	tunnel := "HTTP/1.1 200 OK\r\n\r\n"
	_, err = readHTTPResponse(bufio.NewReader(strings.NewReader(tunnel)), defaultHTTPLimits(), "CONNECT")
	if protocolKind(err) != protocolUnsupportedFeature {
		t.Fatalf("successful CONNECT error = %v, want %s", err, protocolUnsupportedFeature)
	}
}

func TestReadHTTPRequestClassifiesTimeout(t *testing.T) {
	_, err := readHTTPRequest(bufio.NewReader(timeoutReader{}), defaultHTTPLimits())
	if got := protocolKind(err); got != protocolTimeout {
		t.Fatalf("error kind = %q, want %q; err = %v", got, protocolTimeout, err)
	}
}

func TestHTTPParserDeterministicMutationCorpus(t *testing.T) {
	seeds := [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n"),
		[]byte(chunkedRequest("2\r\nab\r\n0\r\n\r\n")),
		[]byte("POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 4\r\n\r\ndata"),
	}
	random := rand.New(rand.NewSource(4107))
	limits := fuzzHTTPLimits()
	for iteration := 0; iteration < 2_000; iteration++ {
		seed := seeds[random.Intn(len(seeds))]
		mutated := append([]byte(nil), seed...)
		switch random.Intn(3) {
		case 0:
			mutated = mutated[:random.Intn(len(mutated)+1)]
		case 1:
			if len(mutated) != 0 {
				mutated[random.Intn(len(mutated))] = byte(random.Intn(256))
			}
		case 2:
			mutated = append(mutated, byte(random.Intn(256)))
		}
		_, _ = readHTTPRequest(bufio.NewReaderSize(bytes.NewReader(mutated), 16), limits)
	}
}

func mustReadRequest(t *testing.T, raw string) *httpRequest {
	t.Helper()
	request, err := readHTTPRequest(bufio.NewReader(strings.NewReader(raw)), defaultHTTPLimits())
	if err != nil {
		t.Fatalf("readHTTPRequest() = %v", err)
	}
	return request
}

func chunkedRequest(body string) string {
	return "POST / HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n\r\n" + body
}

func bodyLimit(maximum int64) func() httpLimits {
	return func() httpLimits {
		limits := defaultHTTPLimits()
		limits.MaxBodyBytes = maximum
		return limits
	}
}

func startLineLimit(maximum int) func() httpLimits {
	return func() httpLimits {
		limits := defaultHTTPLimits()
		limits.MaxStartLineBytes = maximum
		return limits
	}
}

func headerByteLimit(maximum int) func() httpLimits {
	return func() httpLimits {
		limits := defaultHTTPLimits()
		limits.MaxHeaderBytes = maximum
		return limits
	}
}

func headerCountLimit(maximum int) func() httpLimits {
	return func() httpLimits {
		limits := defaultHTTPLimits()
		limits.MaxHeaderFields = maximum
		return limits
	}
}

func chunkCountLimit(maximum int) func() httpLimits {
	return func() httpLimits {
		limits := defaultHTTPLimits()
		limits.MaxChunkCount = maximum
		return limits
	}
}

type fragmentReader struct {
	data     []byte
	pattern  []int
	position int
	step     int
}

func (r *fragmentReader) Read(buffer []byte) (int, error) {
	if r.position >= len(r.data) {
		return 0, io.EOF
	}
	maximum := len(buffer)
	if len(r.pattern) != 0 {
		patternMaximum := r.pattern[r.step%len(r.pattern)]
		r.step++
		if patternMaximum < maximum {
			maximum = patternMaximum
		}
	}
	if remaining := len(r.data) - r.position; remaining < maximum {
		maximum = remaining
	}
	copy(buffer, r.data[r.position:r.position+maximum])
	r.position += maximum
	return maximum, nil
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) {
	return 0, timeoutReadError{}
}

type timeoutReadError struct{}

func (timeoutReadError) Error() string   { return "test timeout" }
func (timeoutReadError) Timeout() bool   { return true }
func (timeoutReadError) Temporary() bool { return true }
