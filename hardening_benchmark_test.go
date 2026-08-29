package main

import (
	"bufio"
	"bytes"
	"testing"
	"time"
)

func BenchmarkReadHTTPRequest(b *testing.B) {
	wire := []byte("POST /bench HTTP/1.1\r\nHost: bench.local\r\nContent-Length: 16\r\nX-Bench: anvil\r\n\r\n0123456789abcdef")
	limits := defaultHTTPLimits()
	b.ReportAllocs()
	for b.Loop() {
		request, err := readHTTPRequest(bufio.NewReader(bytes.NewReader(wire)), limits)
		if err != nil || len(request.Body) != 16 {
			b.Fatalf("parse request: %v", err)
		}
	}
}

func BenchmarkBackendReserveRoundRobin(b *testing.B) {
	pool, err := newBackendPool([]backendConfig{
		{Alias: "a", Address: "127.0.0.1:8001", MaxInFlight: 8},
		{Alias: "b", Address: "127.0.0.1:8002", MaxInFlight: 8},
		{Alias: "c", Address: "127.0.0.1:8003", MaxInFlight: 8},
	})
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reservation, reserveErr := pool.reserveNext()
		if reserveErr != nil {
			b.Fatal(reserveErr)
		}
		reservation.Complete(passiveSuccess, now)
	}
}
