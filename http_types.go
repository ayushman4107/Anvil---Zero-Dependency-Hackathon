package main

import (
	"fmt"
	"strings"
	"sync/atomic"
)

const httpVersion11 = "HTTP/1.1"

const (
	maxHTTPStartLineLimit = 64 * 1024
	maxHTTPHeaderLimit    = 256 * 1024
	maxHTTPHeaderFields   = 4 * 1024
	maxHTTPBodyLimit      = 64 * 1024 * 1024
	maxHTTPChunkLineLimit = 64 * 1024
	maxHTTPChunkCount     = 64 * 1024
	maxHTTPTrailerLimit   = 64 * 1024
	maxHTTPTrailerFields  = 1 * 1024
)

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
	Method     string
	Target     string
	Version    string
	Headers    headerFields
	Trailers   headerFields
	Body       []byte
	BodyMode   bodyMode
	KeepAlive  bool
	RemoteAddr string
}

type responseCommitState struct {
	committed atomic.Bool
}

func (s *responseCommitState) MarkCommitted() {
	if s != nil {
		s.committed.Store(true)
	}
}

func (s *responseCommitState) Committed() bool {
	return s != nil && s.committed.Load()
}

type httpResponse struct {
	Version     string
	StatusCode  int
	Reason      string
	Headers     headerFields
	Trailers    headerFields
	Body        []byte
	BodyMode    bodyMode
	KeepAlive   bool
	Close       bool
	CommitState *responseCommitState
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
	case l.MaxStartLineBytes <= 0 || l.MaxStartLineBytes > maxHTTPStartLineLimit:
		return fmt.Errorf("maximum start-line bytes must be between 1 and %d", maxHTTPStartLineLimit)
	case l.MaxHeaderBytes <= 0 || l.MaxHeaderBytes > maxHTTPHeaderLimit:
		return fmt.Errorf("maximum header bytes must be between 1 and %d", maxHTTPHeaderLimit)
	case l.MaxHeaderFields <= 0 || l.MaxHeaderFields > maxHTTPHeaderFields:
		return fmt.Errorf("maximum header fields must be between 1 and %d", maxHTTPHeaderFields)
	case l.MaxBodyBytes < 0 || l.MaxBodyBytes > maxHTTPBodyLimit:
		return fmt.Errorf("maximum body bytes must be between 0 and %d", maxHTTPBodyLimit)
	case l.MaxChunkLineBytes <= 0 || l.MaxChunkLineBytes > maxHTTPChunkLineLimit:
		return fmt.Errorf("maximum chunk-line bytes must be between 1 and %d", maxHTTPChunkLineLimit)
	case l.MaxChunkCount <= 0 || l.MaxChunkCount > maxHTTPChunkCount:
		return fmt.Errorf("maximum chunk count must be between 1 and %d", maxHTTPChunkCount)
	case l.MaxTrailerBytes <= 0 || l.MaxTrailerBytes > maxHTTPTrailerLimit:
		return fmt.Errorf("maximum trailer bytes must be between 1 and %d", maxHTTPTrailerLimit)
	case l.MaxTrailerFields <= 0 || l.MaxTrailerFields > maxHTTPTrailerFields:
		return fmt.Errorf("maximum trailer fields must be between 1 and %d", maxHTTPTrailerFields)
	default:
		return nil
	}
}
