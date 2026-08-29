package main

import (
	"fmt"
	"strings"
)

const httpVersion11 = "HTTP/1.1"

type bodyMode uint8

const (
	bodyModeNone bodyMode = iota
	bodyModeFixed
	bodyModeChunked
	bodyModeCloseDelimited
)

func (m bodyMode) String() string {
	switch m {
	case bodyModeNone:
		return "none"
	case bodyModeFixed:
		return "fixed"
	case bodyModeChunked:
		return "chunked"
	case bodyModeCloseDelimited:
		return "close-delimited"
	default:
		return "invalid"
	}
}

type headerField struct {
	Name  string
	Value string
}

type headerFields []headerField

func (h headerFields) Values(name string) []string {
	values := make([]string, 0, 1)
	for _, field := range h {
		if strings.EqualFold(field.Name, name) {
			values = append(values, field.Value)
		}
	}
	return values
}

func (h headerFields) First(name string) (string, bool) {
	for _, field := range h {
		if strings.EqualFold(field.Name, name) {
			return field.Value, true
		}
	}
	return "", false
}

func (h headerFields) HasToken(name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

type httpRequest struct {
	Method    string
	Target    string
	Version   string
	Headers   headerFields
	Trailers  headerFields
	Body      []byte
	BodyMode  bodyMode
	KeepAlive bool
}

type httpResponse struct {
	Version    string
	StatusCode int
	Reason     string
	Headers    headerFields
	Trailers   headerFields
	Body       []byte
	BodyMode   bodyMode
	KeepAlive  bool
	Close      bool
}

type httpLimits struct {
	MaxStartLineBytes int
	MaxHeaderBytes    int
	MaxHeaderFields   int
	MaxBodyBytes      int64
	MaxChunkLineBytes int
	MaxChunkCount     int
	MaxTrailerBytes   int
	MaxTrailerFields  int
}

func defaultHTTPLimits() httpLimits {
	return httpLimits{
		MaxStartLineBytes: 8 * 1024,
		MaxHeaderBytes:    32 * 1024,
		MaxHeaderFields:   100,
		MaxBodyBytes:      8 * 1024 * 1024,
		MaxChunkLineBytes: 4 * 1024,
		MaxChunkCount:     16 * 1024,
		MaxTrailerBytes:   8 * 1024,
		MaxTrailerFields:  32,
	}
}

func (l httpLimits) validate() error {
	switch {
	case l.MaxStartLineBytes <= 0:
		return fmt.Errorf("maximum start-line bytes must be greater than zero")
	case l.MaxHeaderBytes <= 0:
		return fmt.Errorf("maximum header bytes must be greater than zero")
	case l.MaxHeaderFields <= 0:
		return fmt.Errorf("maximum header fields must be greater than zero")
	case l.MaxBodyBytes < 0:
		return fmt.Errorf("maximum body bytes must not be negative")
	case l.MaxBodyBytes >= int64(maxInt()):
		return fmt.Errorf("maximum body bytes must fit in platform memory limits")
	case l.MaxChunkLineBytes <= 0:
		return fmt.Errorf("maximum chunk-line bytes must be greater than zero")
	case l.MaxChunkCount <= 0:
		return fmt.Errorf("maximum chunk count must be greater than zero")
	case l.MaxTrailerBytes <= 0:
		return fmt.Errorf("maximum trailer bytes must be greater than zero")
	case l.MaxTrailerFields <= 0:
		return fmt.Errorf("maximum trailer fields must be greater than zero")
	default:
		return nil
	}
}
