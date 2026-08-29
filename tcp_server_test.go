package main

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestTCPServerRejectsBeyondAdmissionLimit(t *testing.T) {
	config := testTCPServerConfig()
	config.MaxConnections = 1

	listener := mustListenLocal(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server, err := newTCPServer(listener, config, func(ctx context.Context, _ *clientConn) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	first := mustDial(t, listener.Addr().String())
	defer first.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first connection was not admitted")
	}

	second := mustDial(t, listener.Addr().String())
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := second.Read(buffer); err == nil {
		t.Fatal("connection beyond admission limit remained open")
	}

	waitFor(t, time.Second, func() bool { return server.Stats().Rejected == 1 })
	stats := server.Stats()
	if stats.PeakActive > 1 {
		t.Fatalf("peak active connections = %d, want at most 1", stats.PeakActive)
	}

	close(release)
	cancel()
	waitServer(t, serverDone)
}

func TestTCPServerGracefulShutdownDrainsConnection(t *testing.T) {
	config := testTCPServerConfig()
	config.ShutdownTimeout = time.Second

	listener := mustListenLocal(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server, err := newTCPServer(listener, config, func(_ context.Context, _ *clientConn) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	client := mustDial(t, listener.Addr().String())
	defer client.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not start")
	}

	cancel()
	waitFor(t, time.Second, func() bool { return server.Stats().State == "draining" })
	close(release)
	waitServer(t, serverDone)

	stats := server.Stats()
	if stats.ForcedClosed != 0 {
		t.Fatalf("forced closes = %d, want 0", stats.ForcedClosed)
	}
	if stats.Active != 0 || stats.Completed != 1 || stats.State != "closed" {
		t.Fatalf("unexpected final stats: %+v", stats)
	}
}

func TestTCPServerForceClosesAfterDrainTimeout(t *testing.T) {
	config := testTCPServerConfig()
	config.ShutdownTimeout = 40 * time.Millisecond
	config.ForceCloseWait = time.Second
	config.ReadTimeout = 5 * time.Second
	config.IdleTimeout = 5 * time.Second

	listener := mustListenLocal(t)
	started := make(chan struct{}, 1)
	server, err := newTCPServer(listener, config, func(_ context.Context, conn *clientConn) error {
		started <- struct{}{}
		buffer := make([]byte, 1)
		_, err := conn.Read(buffer)
		return err
	})
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	client := mustDial(t, listener.Addr().String())
	defer client.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not start")
	}

	cancel()
	waitServer(t, serverDone)
	stats := server.Stats()
	if stats.ForcedClosed != 1 {
		t.Fatalf("forced closes = %d, want 1; stats: %+v", stats.ForcedClosed, stats)
	}
	if stats.Active != 0 || stats.State != "closed" {
		t.Fatalf("unexpected final stats: %+v", stats)
	}
}

func TestTCPServerReclaimsIdleConnection(t *testing.T) {
	config := testTCPServerConfig()
	config.ReadTimeout = 60 * time.Millisecond
	config.IdleTimeout = 60 * time.Millisecond

	listener := mustListenLocal(t)
	server, err := newTCPServer(listener, config, handleEchoConnection)
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	client := mustDial(t, listener.Addr().String())
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := client.Read(buffer); err == nil {
		t.Fatal("idle connection was not reclaimed")
	}

	waitFor(t, time.Second, func() bool { return server.Stats().Completed == 1 })
	stats := server.Stats()
	if stats.HandlerErrors != 1 {
		t.Fatalf("handler errors = %d, want idle timeout recorded once", stats.HandlerErrors)
	}
	cancel()
	waitServer(t, serverDone)
}

func TestTCPServerSlowClientDoesNotBlockFastClient(t *testing.T) {
	config := testTCPServerConfig()
	config.MaxConnections = 2
	config.ReadTimeout = time.Second
	config.IdleTimeout = time.Second

	listener := mustListenLocal(t)
	server, err := newTCPServer(listener, config, handleEchoConnection)
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	slow := mustDial(t, listener.Addr().String())
	defer slow.Close()
	waitFor(t, time.Second, func() bool { return server.Stats().Admitted == 1 })

	fast := mustDial(t, listener.Addr().String())
	defer fast.Close()
	_ = fast.SetDeadline(time.Now().Add(300 * time.Millisecond))
	payload := []byte("fast-client")
	if _, err := fast.Write(payload); err != nil {
		t.Fatalf("fast client write = %v", err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(fast, response); err != nil {
		t.Fatalf("fast client read while slow client is idle = %v", err)
	}
	if string(response) != string(payload) {
		t.Fatalf("fast response = %q, want %q", response, payload)
	}

	_ = fast.Close()
	_ = slow.Close()
	cancel()
	waitServer(t, serverDone)
	if peak := server.Stats().PeakActive; peak != 2 {
		t.Fatalf("peak active connections = %d, want 2", peak)
	}
}

func TestTCPServerCanStartOnlyOnce(t *testing.T) {
	listener := mustListenLocal(t)
	server, err := newTCPServer(listener, testTCPServerConfig(), func(context.Context, *clientConn) error { return nil })
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx); err != nil {
		t.Fatalf("first Serve() = %v", err)
	}
	if err := server.Serve(context.Background()); !errors.Is(err, errServerAlreadyStarted) {
		t.Fatalf("second Serve() = %v, want %v", err, errServerAlreadyStarted)
	}
}

func TestTCPServerContainsHandlerPanic(t *testing.T) {
	listener := mustListenLocal(t)
	server, err := newTCPServer(listener, testTCPServerConfig(), func(context.Context, *clientConn) error {
		panic("test panic")
	})
	if err != nil {
		t.Fatalf("newTCPServer() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := runTCPServer(server, ctx)
	client := mustDial(t, listener.Addr().String())
	_ = client.Close()
	waitFor(t, time.Second, func() bool { return server.Stats().Completed == 1 })
	stats := server.Stats()
	if stats.HandlerErrors != 1 || stats.Active != 0 {
		t.Fatalf("unexpected stats after handler panic: %+v", stats)
	}
	cancel()
	waitServer(t, serverDone)
}

func TestTCPServerRepeatedStartStop(t *testing.T) {
	for range 12 {
		config := testTCPServerConfig()
		listener := mustListenLocal(t)
		server, err := newTCPServer(listener, config, func(_ context.Context, conn *clientConn) error {
			buffer := make([]byte, 1)
			_, err := io.ReadFull(conn, buffer)
			return err
		})
		if err != nil {
			t.Fatalf("newTCPServer() = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		serverDone := runTCPServer(server, ctx)
		client := mustDial(t, listener.Addr().String())
		if _, err := client.Write([]byte{'x'}); err != nil {
			t.Fatalf("client write = %v", err)
		}
		_ = client.Close()
		waitFor(t, time.Second, func() bool { return server.Stats().Completed == 1 })
		cancel()
		waitServer(t, serverDone)
		if stats := server.Stats(); stats.Active != 0 || stats.State != "closed" {
			t.Fatalf("server retained lifecycle state: %+v", stats)
		}
	}
}

func testTCPServerConfig() tcpServerConfig {
	return tcpServerConfig{
		MaxConnections:  8,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		ShutdownTimeout: 250 * time.Millisecond,
		ForceCloseWait:  time.Second,
	}
}

func mustListenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() = %v", err)
	}
	return listener
}

func mustDial(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("net.DialTimeout() = %v", err)
	}
	return conn
}

func runTCPServer(server *tcpServer, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx)
	}()
	return done
}

func waitServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}
