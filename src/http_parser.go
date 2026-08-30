package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
)

func readHTTPRequest(reader *bufio.Reader, limits httpLimits) (*httpRequest, error) {
	if reader == nil {
		return nil, newProtocolError(protocolInvalidGeneratedMessage, "request", "reader is required")
	}
	if err := limits.validate(); err != nil {
		return nil, &protocolError{Kind: protocolLimitExceeded, Section: "limits", Err: err}
	}

	line, _, err := readStrictLine(reader, limits.MaxStartLineBytes, "request line")
	if err != nil {
		return nil, err
	}
	method, target, version, err := parseRequestLine(line)
	if err != nil {
		return nil, err
	}
	headers, err := readHeaderBlock(reader, limits.MaxHeaderBytes, limits.MaxHeaderFields, "request headers")
	if err != nil {
		return nil, err
	}
	if err := validateRequestHeaders(method, headers); err != nil {
		return nil, err
	}

	mode, length, err := determineBodyFraming(headers, limits.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	request := &httpRequest{
		Method:    method,
		Target:    target,
		Version:   version,
		Headers:   headers,
		BodyMode:  mode,
		KeepAlive: !headers.HasToken("Connection", "close"),
	}

	switch mode {
	case bodyModeNone:
	case bodyModeFixed:
		request.Body, err = readFixedBody(reader, length, "request body")
	case bodyModeChunked:
		request.Body, request.Trailers, err = readChunkedBody(reader, limits, "request body")
	default:
		err = newProtocolError(protocolAmbiguousFraming, "request body", "invalid request body mode")
	}
	if err != nil {
		return nil, err
	}
	return request, nil
}

func readHTTPResponse(reader *bufio.Reader, limits httpLimits, requestMethod string) (*httpResponse, error) {
	if reader == nil {
		return nil, newProtocolError(protocolInvalidGeneratedMessage, "response", "reader is required")
	}
	if err := limits.validate(); err != nil {
		return nil, &protocolError{Kind: protocolLimitExceeded, Section: "limits", Err: err}
	}

	line, _, err := readStrictLine(reader, limits.MaxStartLineBytes, "status line")
	if err != nil {
		return nil, err
	}
	version, statusCode, reason, err := parseStatusLine(line)
	if err != nil {
		return nil, err
	}
	headers, err := readHeaderBlock(reader, limits.MaxHeaderBytes, limits.MaxHeaderFields, "response headers")
	if err != nil {
		return nil, err
	}
	if err := validateConnectionFields(headers, "response headers"); err != nil {
		return nil, err
	}
	if err := validateResponseFramingHeaders(statusCode, headers); err != nil {
		return nil, err
	}
	if successfulConnectResponse(requestMethod, statusCode) || statusCode == 101 || headers.HasToken("Connection", "upgrade") || len(headers.Values("Upgrade")) != 0 {
		return nil, newProtocolError(protocolUnsupportedFeature, "response headers", "tunnels and protocol upgrades are unsupported")
	}

	mode, length, err := determineBodyFraming(headers, limits.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	noBody := responseMustNotHaveBody(requestMethod, statusCode)
	response := &httpResponse{
		Version:    version,
		StatusCode: statusCode,
		Reason:     reason,
		Headers:    headers,
		BodyMode:   mode,
	}
	if noBody {
		response.BodyMode = bodyModeNone
		response.KeepAlive = !headers.HasToken("Connection", "close")
		return response, nil
	}

	switch mode {
	case bodyModeNone:
		response.BodyMode = bodyModeCloseDelimited
		response.Body, err = readCloseDelimitedBody(reader, limits.MaxBodyBytes, "response body")
	case bodyModeFixed:
		response.Body, err = readFixedBody(reader, length, "response body")
	case bodyModeChunked:
		response.Body, response.Trailers, err = readChunkedBody(reader, limits, "response body")
	default:
		err = newProtocolError(protocolAmbiguousFraming, "response body", "invalid response body mode")
	}
	if err != nil {
		return nil, err
	}
	response.KeepAlive = response.BodyMode != bodyModeCloseDelimited && !headers.HasToken("Connection", "close")
	return response, nil
}

func validateResponseFramingHeaders(statusCode int, headers headerFields) error {
	if statusCode >= 100 && statusCode < 200 || statusCode == 204 {
		if len(headers.Values("Content-Length")) != 0 || len(headers.Values("Transfer-Encoding")) != 0 {
			return newProtocolError(protocolMalformedHeader, "response headers", "1xx and 204 responses cannot declare message framing")
		}
	}
	return nil
}

func readStrictLine(reader *bufio.Reader, maximum int, section string) ([]byte, int, error) {
	if maximum < 2 {
		return nil, 0, newProtocolError(protocolLimitExceeded, section, "line limit is too small")
	}

	line := make([]byte, 0, min(maximum, 256))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line) > maximum-len(fragment) {
			return nil, len(line) + len(fragment), newProtocolError(protocolLimitExceeded, section, "line exceeds configured byte limit")
		}
		line = append(line, fragment...)

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, len(line), protocolErrorFromRead(section, err, len(line) != 0)
		}
		break
	}

	if len(line) < 2 || line[len(line)-2] != '\r' || line[len(line)-1] != '\n' {
		return nil, len(line), newProtocolError(lineErrorKind(section), section, "line must end with CRLF")
	}
	content := line[:len(line)-2]
	if bytes.IndexByte(content, '\r') >= 0 || bytes.IndexByte(content, '\n') >= 0 {
		return nil, len(line), newProtocolError(lineErrorKind(section), section, "embedded line break")
	}
	return content, len(line), nil
}

func lineErrorKind(section string) protocolErrorKind {
	if section == "request line" || section == "status line" {
		return protocolMalformedStartLine
	}
	if strings.Contains(section, "chunk") {
		return protocolInvalidChunk
	}
	return protocolMalformedHeader
}

func parseRequestLine(line []byte) (string, string, string, error) {
	parts := bytes.Split(line, []byte{' '})
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return "", "", "", newProtocolError(protocolMalformedStartLine, "request line", "expected method, target, and version")
	}
	if !validToken(parts[0]) {
		return "", "", "", newProtocolError(protocolMalformedStartLine, "request line", "method is not an HTTP token")
	}
	method := string(parts[0])
	target := string(parts[1])
	version := string(parts[2])
	if version != httpVersion11 {
		return "", "", "", newProtocolError(protocolUnsupportedVersion, "request line", "only HTTP/1.1 is supported")
	}
	if strings.EqualFold(method, "CONNECT") {
		return "", "", "", newProtocolError(protocolUnsupportedTarget, "request line", "CONNECT is unsupported")
	}
	if !validRequestTarget(method, parts[1]) {
		return "", "", "", newProtocolError(protocolUnsupportedTarget, "request line", "only origin-form and OPTIONS * are supported")
	}
	return method, target, version, nil
}

func parseStatusLine(line []byte) (string, int, string, error) {
	if len(line) < 13 || string(line[:8]) != httpVersion11 || line[8] != ' ' || line[12] != ' ' {
		return "", 0, "", newProtocolError(protocolMalformedStartLine, "status line", "invalid HTTP/1.1 status line")
	}
	codeBytes := line[9:12]
	if codeBytes[0] < '0' || codeBytes[0] > '9' || codeBytes[1] < '0' || codeBytes[1] > '9' || codeBytes[2] < '0' || codeBytes[2] > '9' {
		return "", 0, "", newProtocolError(protocolMalformedStartLine, "status line", "status code must contain three digits")
	}
	statusCode := int(codeBytes[0]-'0')*100 + int(codeBytes[1]-'0')*10 + int(codeBytes[2]-'0')
	if statusCode < 100 || statusCode > 599 {
		return "", 0, "", newProtocolError(protocolMalformedStartLine, "status line", "status code is outside 100-599")
	}
	reason := line[13:]
	if !validReasonPhrase(reason) {
		return "", 0, "", newProtocolError(protocolMalformedStartLine, "status line", "invalid reason phrase")
	}
	return httpVersion11, statusCode, string(reason), nil
}

func readHeaderBlock(reader *bufio.Reader, maximumBytes, maximumFields int, section string) (headerFields, error) {
	headers := make(headerFields, 0, min(maximumFields, 16))
	consumed := 0
	for {
		remaining := maximumBytes - consumed
		if remaining < 2 {
			return nil, newProtocolError(protocolLimitExceeded, section, "headers exceed configured byte limit")
		}
		line, lineBytes, err := readStrictLine(reader, remaining, section)
		consumed += lineBytes
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			return headers, nil
		}
		if len(headers) >= maximumFields {
			return nil, newProtocolError(protocolLimitExceeded, section, "header field count exceeds configured limit")
		}
		field, err := parseHeaderField(line, section)
		if err != nil {
			return nil, err
		}
		headers = append(headers, field)
	}
}

func parseHeaderField(line []byte, section string) (headerField, error) {
	if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
		return headerField{}, newProtocolError(protocolMalformedHeader, section, "obsolete line folding is forbidden")
	}
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 || !validToken(line[:colon]) {
		return headerField{}, newProtocolError(protocolMalformedHeader, section, "invalid field name or whitespace before colon")
	}
	value := trimOWS(line[colon+1:])
	if !validFieldValue(value) {
		return headerField{}, newProtocolError(protocolMalformedHeader, section, "field value contains a forbidden control byte")
	}
	return headerField{Name: string(line[:colon]), Value: string(value)}, nil
}

func validateRequestHeaders(method string, headers headerFields) error {
	hostValues := headers.Values("Host")
	if len(hostValues) == 0 {
		return newProtocolError(protocolMissingHost, "request headers", "HTTP/1.1 requires Host")
	}
	if len(hostValues) != 1 || !validHost(hostValues[0]) {
		return newProtocolError(protocolMalformedHeader, "request headers", "Host must be one valid authority value")
	}
	if err := validateConnectionFields(headers, "request headers"); err != nil {
		return err
	}
	if headers.HasToken("Connection", "upgrade") || len(headers.Values("Upgrade")) != 0 {
		return newProtocolError(protocolUnsupportedFeature, "request headers", "protocol upgrades are unsupported")
	}
	if len(headers.Values("Expect")) != 0 {
		return newProtocolError(protocolUnsupportedFeature, "request headers", "Expect is unsupported")
	}
	_ = method
	return nil
}

func validateConnectionFields(headers headerFields, section string) error {
	for _, value := range headers.Values("Connection") {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || !validToken([]byte(part)) {
				return newProtocolError(protocolMalformedHeader, section, "Connection contains an invalid token")
			}
		}
	}
	return nil
}

func determineBodyFraming(headers headerFields, maximumBody int64) (bodyMode, int64, error) {
	transferValues := headers.Values("Transfer-Encoding")
	lengthValues := headers.Values("Content-Length")
	if len(transferValues) != 0 && len(lengthValues) != 0 {
		return bodyModeNone, 0, newProtocolError(protocolAmbiguousFraming, "message framing", "Transfer-Encoding and Content-Length cannot coexist")
	}
	if len(transferValues) != 0 {
		if !onlyChunkedTransferCoding(transferValues) {
			return bodyModeNone, 0, newProtocolError(protocolUnsupportedTransferCoding, "message framing", "only a single chunked coding is supported")
		}
		return bodyModeChunked, 0, nil
	}
	if len(lengthValues) == 0 {
		return bodyModeNone, 0, nil
	}
	length, err := parseContentLengths(lengthValues)
	if err != nil {
		return bodyModeNone, 0, err
	}
	if length > maximumBody {
		return bodyModeNone, 0, newProtocolError(protocolBodyTooLarge, "message framing", "declared body exceeds configured limit")
	}
	return bodyModeFixed, length, nil
}

func parseContentLengths(values []string) (int64, error) {
	var expected uint64
	seen := false
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" || !decimalDigits(item) {
				return 0, newProtocolError(protocolInvalidContentLength, "message framing", "Content-Length must contain decimal digits")
			}
			parsed, err := strconv.ParseUint(item, 10, 63)
			if err != nil {
				return 0, &protocolError{Kind: protocolInvalidContentLength, Section: "message framing", Err: err}
			}
			if seen && parsed != expected {
				return 0, newProtocolError(protocolAmbiguousFraming, "message framing", "Content-Length values conflict")
			}
			expected = parsed
			seen = true
		}
	}
	if !seen {
		return 0, newProtocolError(protocolInvalidContentLength, "message framing", "Content-Length is empty")
	}
	return int64(expected), nil
}

func onlyChunkedTransferCoding(values []string) bool {
	if len(values) != 1 {
		return false
	}
	parts := strings.Split(values[0], ",")
	return len(parts) == 1 && strings.EqualFold(strings.TrimSpace(parts[0]), "chunked")
}

func readFixedBody(reader io.Reader, length int64, section string) ([]byte, error) {
	if length == 0 {
		return []byte{}, nil
	}
	if length < 0 || length > int64(maxInt()) {
		return nil, newProtocolError(protocolBodyTooLarge, section, "body cannot be represented on this platform")
	}
	body := make([]byte, int(length))
	_, err := io.ReadFull(reader, body)
	if err != nil {
		return nil, protocolErrorFromRead(section, err, true)
	}
	return body, nil
}

func readCloseDelimitedBody(reader io.Reader, maximum int64, section string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, protocolErrorFromRead(section, err, true)
	}
	if int64(len(body)) > maximum {
		return nil, newProtocolError(protocolBodyTooLarge, section, "close-delimited body exceeds configured limit")
	}
	return body, nil
}

func readChunkedBody(reader *bufio.Reader, limits httpLimits, section string) ([]byte, headerFields, error) {
	body := make([]byte, 0)
	for chunkCount := 1; ; chunkCount++ {
		if chunkCount > limits.MaxChunkCount {
			return nil, nil, newProtocolError(protocolLimitExceeded, section, "chunk count exceeds configured limit")
		}
		line, _, err := readStrictLine(reader, limits.MaxChunkLineBytes, "chunk size")
		if err != nil {
			return nil, nil, err
		}
		size, err := parseChunkSizeLine(line)
		if err != nil {
			return nil, nil, err
		}
		if size == 0 {
			trailers, err := readHeaderBlock(reader, limits.MaxTrailerBytes, limits.MaxTrailerFields, "trailers")
			if err != nil {
				return nil, nil, err
			}
			if err := validateTrailers(trailers); err != nil {
				return nil, nil, err
			}
			return body, trailers, nil
		}
		if size > uint64(limits.MaxBodyBytes)-uint64(len(body)) || size > uint64(maxInt()-len(body)) {
			return nil, nil, newProtocolError(protocolBodyTooLarge, section, "decoded chunked body exceeds configured limit")
		}

		start := len(body)
		body = append(body, make([]byte, int(size))...)
		if _, err := io.ReadFull(reader, body[start:]); err != nil {
			return nil, nil, protocolErrorFromRead(section, err, true)
		}
		ending := [2]byte{}
		if _, err := io.ReadFull(reader, ending[:]); err != nil {
			return nil, nil, protocolErrorFromRead(section, err, true)
		}
		if ending != [2]byte{'\r', '\n'} {
			return nil, nil, newProtocolError(protocolInvalidChunk, section, "chunk data must end with CRLF")
		}
	}
}

func parseChunkSizeLine(line []byte) (uint64, error) {
	semicolon := bytes.IndexByte(line, ';')
	sizeBytes := line
	if semicolon >= 0 {
		sizeBytes = line[:semicolon]
		if !validChunkExtensions(line[semicolon:]) {
			return 0, newProtocolError(protocolInvalidChunk, "chunk size", "invalid chunk extension")
		}
	}
	if len(sizeBytes) == 0 {
		return 0, newProtocolError(protocolInvalidChunk, "chunk size", "missing hexadecimal size")
	}
	var size uint64
	for _, current := range sizeBytes {
		digit, ok := hexadecimalValue(current)
		if !ok || size > (math.MaxUint64-digit)/16 {
			return 0, newProtocolError(protocolInvalidChunk, "chunk size", "invalid or overflowing hexadecimal size")
		}
		size = size*16 + digit
	}
	return size, nil
}

func validChunkExtensions(extensions []byte) bool {
	position := 0
	for position < len(extensions) {
		if extensions[position] != ';' {
			return false
		}
		position++
		position = skipOWS(extensions, position)
		start := position
		for position < len(extensions) && isTokenByte(extensions[position]) {
			position++
		}
		if start == position {
			return false
		}
		position = skipOWS(extensions, position)
		if position < len(extensions) && extensions[position] == '=' {
			position++
			position = skipOWS(extensions, position)
			if position >= len(extensions) {
				return false
			}
			if extensions[position] == '"' {
				var ok bool
				position, ok = consumeQuotedString(extensions, position)
				if !ok {
					return false
				}
			} else {
				start = position
				for position < len(extensions) && isTokenByte(extensions[position]) {
					position++
				}
				if start == position {
					return false
				}
			}
			position = skipOWS(extensions, position)
		}
		if position < len(extensions) && extensions[position] != ';' {
			return false
		}
	}
	return true
}

func consumeQuotedString(value []byte, position int) (int, bool) {
	position++
	for position < len(value) {
		current := value[position]
		switch {
		case current == '"':
			return position + 1, true
		case current == '\\':
			position++
			if position >= len(value) || !validQuotedByte(value[position]) {
				return position, false
			}
		case !validQuotedByte(current):
			return position, false
		}
		position++
	}
	return position, false
}

func validateTrailers(trailers headerFields) error {
	for _, field := range trailers {
		switch strings.ToLower(field.Name) {
		case "content-length", "transfer-encoding", "host", "connection", "trailer", "te", "upgrade", "proxy-connection", "keep-alive":
			return newProtocolError(protocolMalformedHeader, "trailers", "framing or connection field is forbidden in trailers")
		}
	}
	return nil
}

func validRequestTarget(method string, target []byte) bool {
	if bytes.Equal(target, []byte{'*'}) {
		return strings.EqualFold(method, "OPTIONS")
	}
	if len(target) == 0 || target[0] != '/' {
		return false
	}
	for position := 0; position < len(target); position++ {
		current := target[position]
		if current < 0x21 || current > 0x7e {
			return false
		}
		if current == '#' || current == '\\' {
			return false
		}
		if current == '%' {
			if position+2 >= len(target) {
				return false
			}
			if _, ok := hexadecimalValue(target[position+1]); !ok {
				return false
			}
			if _, ok := hexadecimalValue(target[position+2]); !ok {
				return false
			}
			position += 2
		}
	}
	return true
}

func validHost(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}

	host := value
	port := ""
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing <= 1 || net.ParseIP(host[1:closing]) == nil {
			return false
		}
		remainder := host[closing+1:]
		if remainder != "" {
			if !strings.HasPrefix(remainder, ":") {
				return false
			}
			port = remainder[1:]
		}
		if strings.Contains(host[closing+1:], "]") {
			return false
		}
	} else {
		if strings.Count(host, ":") > 1 {
			return false
		}
		if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
			port = host[colon+1:]
			host = host[:colon]
		}
		if !validRegName(host) {
			return false
		}
	}
	if port != "" {
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed > 65535 {
			return false
		}
	} else if strings.HasSuffix(value, ":") {
		return false
	}
	return true
}

func validRegName(host string) bool {
	if host == "" {
		return false
	}
	for position := 0; position < len(host); position++ {
		current := host[position]
		if current == '%' {
			if position+2 >= len(host) {
				return false
			}
			if _, ok := hexadecimalValue(host[position+1]); !ok {
				return false
			}
			if _, ok := hexadecimalValue(host[position+2]); !ok {
				return false
			}
			position += 2
			continue
		}
		if current >= '0' && current <= '9' || current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' || strings.ContainsRune("-._~!$&'()*+,;=", rune(current)) {
			continue
		}
		return false
	}
	return true
}

func validToken(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, current := range value {
		if !isTokenByte(current) {
			return false
		}
	}
	return true
}

func isTokenByte(value byte) bool {
	if value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(value))
}

func validFieldValue(value []byte) bool {
	for _, current := range value {
		if current == '\t' || current >= 0x20 && current != 0x7f {
			continue
		}
		return false
	}
	return true
}

func validReasonPhrase(value []byte) bool {
	return validFieldValue(value)
}

func validQuotedByte(value byte) bool {
	return value == '\t' || value >= 0x20 && value != 0x7f
}

func trimOWS(value []byte) []byte {
	return bytes.Trim(value, " \t")
}

func skipOWS(value []byte, position int) int {
	for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
		position++
	}
	return position
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, current := range []byte(value) {
		if current < '0' || current > '9' {
			return false
		}
	}
	return true
}

func hexadecimalValue(value byte) (uint64, bool) {
	switch {
	case value >= '0' && value <= '9':
		return uint64(value - '0'), true
	case value >= 'a' && value <= 'f':
		return uint64(value-'a') + 10, true
	case value >= 'A' && value <= 'F':
		return uint64(value-'A') + 10, true
	default:
		return 0, false
	}
}

func responseMustNotHaveBody(requestMethod string, statusCode int) bool {
	return strings.EqualFold(requestMethod, "HEAD") || statusCode >= 100 && statusCode < 200 || statusCode == 204 || statusCode == 304
}

func successfulConnectResponse(requestMethod string, statusCode int) bool {
	return strings.EqualFold(requestMethod, "CONNECT") && statusCode >= 200 && statusCode < 300
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
