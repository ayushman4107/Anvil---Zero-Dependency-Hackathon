package main

import (
	"errors"
	"io"
	"net"
)

type protocolErrorKind string

const (
	protocolMalformedStartLine        protocolErrorKind = "malformed_start_line"
	protocolUnsupportedVersion        protocolErrorKind = "unsupported_version"
	protocolUnsupportedTarget         protocolErrorKind = "unsupported_target"
	protocolUnsupportedFeature        protocolErrorKind = "unsupported_feature"
	protocolMalformedHeader           protocolErrorKind = "malformed_header"
	protocolMissingHost               protocolErrorKind = "missing_host"
	protocolAmbiguousFraming          protocolErrorKind = "ambiguous_framing"
	protocolInvalidContentLength      protocolErrorKind = "invalid_content_length"
	protocolUnsupportedTransferCoding protocolErrorKind = "unsupported_transfer_coding"
	protocolInvalidChunk              protocolErrorKind = "invalid_chunk"
	protocolLimitExceeded             protocolErrorKind = "limit_exceeded"
	protocolBodyTooLarge              protocolErrorKind = "body_too_large"
	protocolIncompleteMessage         protocolErrorKind = "incomplete_message"
	protocolTimeout                   protocolErrorKind = "timeout"
	protocolInvalidGeneratedMessage   protocolErrorKind = "invalid_generated_message"
)

type protocolError struct {
	Kind    protocolErrorKind
	Section string
	Detail  string
	Err     error
}

func (e *protocolError) Error() string {
	message := "HTTP protocol error"
	if e.Section != "" {
		message += " in " + e.Section
	}
	message += ": " + string(e.Kind)
	if e.Detail != "" {
		message += " (" + e.Detail + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *protocolError) Unwrap() error {
	return e.Err
}

func newProtocolError(kind protocolErrorKind, section, detail string) error {
	return &protocolError{Kind: kind, Section: section, Detail: detail}
}

func protocolErrorFromRead(section string, err error, partial bool) error {
	if err == nil {
		return nil
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return &protocolError{Kind: protocolTimeout, Section: section, Err: err}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		if !partial && errors.Is(err, io.EOF) {
			return io.EOF
		}
		return &protocolError{Kind: protocolIncompleteMessage, Section: section, Err: err}
	}
	return &protocolError{Kind: protocolIncompleteMessage, Section: section, Err: err}
}

func protocolKind(err error) protocolErrorKind {
	var protocolErr *protocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Kind
	}
	return ""
}
