package main

import (
	"context"
	"io"
	"net"
)

// serveEcho exercises Anvil's Phase 1 TCP lifecycle through a deliberately
// small byte-echo handler. It is a development milestone, not the final proxy
// data path.
func serveEcho(ctx context.Context, listener net.Listener, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	server, err := newTCPServer(listener, cfg.tcpServerConfig(), handleEchoConnection)
	if err != nil {
		return err
	}
	return server.Serve(ctx)
}

func handleEchoConnection(_ context.Context, conn *clientConn) error {
	_, err := io.Copy(conn, conn)
	return err
}
