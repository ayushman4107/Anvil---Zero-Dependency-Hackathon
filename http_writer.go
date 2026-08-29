package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func writeHTTPRequest(writer io.Writer, request *httpRequest) error {
	if writer == nil || request == nil {
		return newProtocolError(protocolInvalidGeneratedMessage, "request writer", "writer and request are required")
	}
	version := request.Version
	if version == "" {
		version = httpVersion11
	}
	line := []byte(request.Method + " " + request.Target + " " + version)
	method, target, parsedVersion, err := parseRequestLine(line)
	if err != nil || method != request.Method || target != request.Target || parsedVersion != version {
		if err != nil {
			return err
		}
		return newProtocolError(protocolInvalidGeneratedMessage, "request writer", "request line changed during validation")
	}
	if err := validateGeneratedFields(request.Headers, "request headers"); err != nil {
		return err
	}
	if err := validateRequestHeaders(request.Method, request.Headers); err != nil {
		return err
	}
	if err := validateTrailers(request.Trailers); err != nil {
		return err
	}

	mode := request.BodyMode
	if mode == bodyModeNone && len(request.Body) != 0 {
		mode = bodyModeFixed
	}
	if mode == bodyModeCloseDelimited {
		return newProtocolError(protocolInvalidGeneratedMessage, "request writer", "request bodies cannot be close-delimited")
	}
	headers := withoutFramingHeaders(request.Headers)
	switch mode {
	case bodyModeNone:
	case bodyModeFixed:
		headers = append(headers, headerField{Name: "Content-Length", Value: strconv.Itoa(len(request.Body))})
	case bodyModeChunked:
		headers = append(headers, headerField{Name: "Transfer-Encoding", Value: "chunked"})
		headers = appendTrailerDeclaration(headers, request.Trailers)
	default:
		return newProtocolError(protocolInvalidGeneratedMessage, "request writer", "invalid body mode")
	}

	if err := writeAll(writer, []byte(request.Method+" "+request.Target+" "+version+"\r\n")); err != nil {
		return err
	}
	if err := writeHeaderBlock(writer, headers); err != nil {
		return err
	}
	return writeMessageBody(writer, mode, request.Body, request.Trailers)
}

func writeHTTPResponse(writer io.Writer, response *httpResponse, requestMethod string) error {
	if writer == nil || response == nil {
		return newProtocolError(protocolInvalidGeneratedMessage, "response writer", "writer and response are required")
	}
	version := response.Version
	if version == "" {
		version = httpVersion11
	}
	if version != httpVersion11 || response.StatusCode < 100 || response.StatusCode > 599 {
		return newProtocolError(protocolInvalidGeneratedMessage, "response writer", "invalid HTTP version or status code")
	}
	reason := response.Reason
	if reason == "" {
		reason = defaultReasonPhrase(response.StatusCode)
	}
	if !validReasonPhrase([]byte(reason)) {
		return newProtocolError(protocolInvalidGeneratedMessage, "response writer", "invalid reason phrase")
	}
	if err := validateGeneratedFields(response.Headers, "response headers"); err != nil {
		return err
	}
	if err := validateConnectionFields(response.Headers, "response headers"); err != nil {
		return err
	}
	if strings.EqualFold(requestMethod, "CONNECT") || response.StatusCode == 101 || response.Headers.HasToken("Connection", "upgrade") || len(response.Headers.Values("Upgrade")) != 0 {
		return newProtocolError(protocolUnsupportedFeature, "response writer", "tunnels and protocol upgrades are unsupported")
	}
	if err := validateTrailers(response.Trailers); err != nil {
		return err
	}

	noBody := responseMustNotHaveBody(requestMethod, response.StatusCode)
	mode := response.BodyMode
	if mode == bodyModeNone && !noBody {
		mode = bodyModeFixed
	}
	if mode == bodyModeCloseDelimited {
		return newProtocolError(protocolInvalidGeneratedMessage, "response writer", "generated responses must use fixed or chunked framing")
	}

	headers := withoutFramingHeaders(response.Headers)
	if response.Close && !headers.HasToken("Connection", "close") {
		headers = append(headers, headerField{Name: "Connection", Value: "close"})
	}
	if noBody {
		mode = bodyModeNone
		if strings.EqualFold(requestMethod, "HEAD") || response.StatusCode == 304 {
			headers = append(headers, headerField{Name: "Content-Length", Value: strconv.Itoa(len(response.Body))})
		}
	} else {
		switch mode {
		case bodyModeFixed:
			headers = append(headers, headerField{Name: "Content-Length", Value: strconv.Itoa(len(response.Body))})
		case bodyModeChunked:
			headers = append(headers, headerField{Name: "Transfer-Encoding", Value: "chunked"})
			headers = appendTrailerDeclaration(headers, response.Trailers)
		default:
			return newProtocolError(protocolInvalidGeneratedMessage, "response writer", "invalid body mode")
		}
	}

	statusLine := fmt.Sprintf("%s %03d %s\r\n", version, response.StatusCode, reason)
	if err := writeAll(writer, []byte(statusLine)); err != nil {
		return err
	}
	if err := writeHeaderBlock(writer, headers); err != nil {
		return err
	}
	if noBody {
		return nil
	}
	return writeMessageBody(writer, mode, response.Body, response.Trailers)
}

func writeMessageBody(writer io.Writer, mode bodyMode, body []byte, trailers headerFields) error {
	switch mode {
	case bodyModeNone:
		return nil
	case bodyModeFixed:
		return writeAll(writer, body)
	case bodyModeChunked:
		chunked, err := newChunkedBodyWriter(writer, trailers)
		if err != nil {
			return err
		}
		if len(body) != 0 {
			if _, err := chunked.Write(body); err != nil {
				return err
			}
		}
		return chunked.Close()
	default:
		return newProtocolError(protocolInvalidGeneratedMessage, "message writer", "unsupported body mode")
	}
}

type chunkedBodyWriter struct {
	writer   io.Writer
	trailers headerFields
	closed   bool
}

func newChunkedBodyWriter(writer io.Writer, trailers headerFields) (*chunkedBodyWriter, error) {
	if writer == nil {
		return nil, newProtocolError(protocolInvalidGeneratedMessage, "chunked writer", "writer is required")
	}
	if err := validateGeneratedFields(trailers, "trailers"); err != nil {
		return nil, err
	}
	if err := validateTrailers(trailers); err != nil {
		return nil, err
	}
	return &chunkedBodyWriter{writer: writer, trailers: append(headerFields(nil), trailers...)}, nil
}

func (w *chunkedBodyWriter) Write(data []byte) (int, error) {
	if w.closed {
		return 0, newProtocolError(protocolInvalidGeneratedMessage, "chunked writer", "write after close")
	}
	if len(data) == 0 {
		return 0, nil
	}
	header := strconv.FormatInt(int64(len(data)), 16) + "\r\n"
	if err := writeAll(w.writer, []byte(header)); err != nil {
		return 0, err
	}
	if err := writeAll(w.writer, data); err != nil {
		return 0, err
	}
	if err := writeAll(w.writer, []byte("\r\n")); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *chunkedBodyWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := writeAll(w.writer, []byte("0\r\n")); err != nil {
		return err
	}
	return writeHeaderBlock(w.writer, w.trailers)
}

func validateGeneratedFields(fields headerFields, section string) error {
	for _, field := range fields {
		if !validToken([]byte(field.Name)) {
			return newProtocolError(protocolInvalidGeneratedMessage, section, "invalid field name")
		}
		if !validFieldValue([]byte(field.Value)) || strings.ContainsAny(field.Value, "\r\n") {
			return newProtocolError(protocolInvalidGeneratedMessage, section, "invalid field value")
		}
	}
	return nil
}

func withoutFramingHeaders(fields headerFields) headerFields {
	filtered := make(headerFields, 0, len(fields)+2)
	for _, field := range fields {
		if strings.EqualFold(field.Name, "Content-Length") || strings.EqualFold(field.Name, "Transfer-Encoding") || strings.EqualFold(field.Name, "Trailer") {
			continue
		}
		filtered = append(filtered, field)
	}
	return filtered
}

func appendTrailerDeclaration(headers, trailers headerFields) headerFields {
	if len(trailers) == 0 {
		return headers
	}
	names := make([]string, 0, len(trailers))
	seen := make(map[string]struct{}, len(trailers))
	for _, trailer := range trailers {
		key := strings.ToLower(trailer.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, trailer.Name)
	}
	return append(headers, headerField{Name: "Trailer", Value: strings.Join(names, ", ")})
}

func writeHeaderBlock(writer io.Writer, headers headerFields) error {
	for _, field := range headers {
		if err := writeAll(writer, []byte(field.Name+": "+field.Value+"\r\n")); err != nil {
			return err
		}
	}
	return writeAll(writer, []byte("\r\n"))
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func defaultReasonPhrase(statusCode int) string {
	switch statusCode {
	case 100:
		return "Continue"
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 204:
		return "No Content"
	case 304:
		return "Not Modified"
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 413:
		return "Content Too Large"
	case 417:
		return "Expectation Failed"
	case 500:
		return "Internal Server Error"
	case 501:
		return "Not Implemented"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Gateway Timeout"
	default:
		return "Status"
	}
}
