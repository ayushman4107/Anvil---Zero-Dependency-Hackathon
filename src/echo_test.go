package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestServeEchoHandlesConcurrentClients(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.MaxConnections = 32
	cfg.IdleTimeoutMS = 2_000

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("net.Listen() = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveEcho(ctx, listener, cfg)
	}()

	const clientCount = 16
	start := make(chan struct{})
	errorsByClient := make(chan error, clientCount)
	var clients sync.WaitGroup
	clients.Add(clientCount)

	for i := range clientCount {
		go func(id int) {
			defer clients.Done()
			<-start
			payload := bytes.Repeat([]byte(fmt.Sprintf("client-%02d|", id)), 128)
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				errorsByClient <- fmt.Errorf("client %d dial: %w", id, err)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

			if _, err := conn.Write(payload); err != nil {
				errorsByClient <- fmt.Errorf("client %d write: %w", id, err)
				return
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil {
				errorsByClient <- fmt.Errorf("client %d read: %w", id, err)
				return
			}
			if !bytes.Equal(got, payload) {
				errorsByClient <- fmt.Errorf("client %d response mismatch", id)
			}
		}(i)
	}

	close(start)
	clients.Wait()
	close(errorsByClient)
	for err := range errorsByClient {
		if err != nil {
			t.Error(err)
		}
	}

	cancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("serveEcho() = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveEcho did not stop after cancellation")
	}
}
