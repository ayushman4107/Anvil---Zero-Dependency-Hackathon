package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// serveEcho is a deliberately small proof that Anvil owns a raw TCP listener
// and services clients concurrently. It is a development milestone, not part
// of the final proxy data path.
func serveEcho(ctx context.Context, listener net.Listener, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	limit := make(chan struct{}, cfg.MaxConnections)
	var clients sync.WaitGroup
	stopWatcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stopWatcher:
		}
	}()
	defer close(stopWatcher)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				clients.Wait()
				return nil
			}
			clients.Wait()
			return fmt.Errorf("accept connection: %w", err)
		}

		select {
		case limit <- struct{}{}:
			clients.Add(1)
			go func() {
				defer clients.Done()
				defer func() { <-limit }()
				handleEchoConnection(conn, cfg.idleTimeout())
			}()
		default:
			_ = conn.Close()
		}
	}
}

func handleEchoConnection(conn net.Conn, idleTimeout time.Duration) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(idleTimeout))
	_, _ = io.Copy(conn, conn)
}
