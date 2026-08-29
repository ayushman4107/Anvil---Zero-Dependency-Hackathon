package main

import (
	"context"
	"net"
	"time"
)

type idleUpstreamConnection struct {
	connection net.Conn
	idleSince  time.Time
}

func (b *proxyBackend) acquireConnection(ctx context.Context, config proxyConfig) (net.Conn, bool, error) {
	now := config.Now()
	for {
		b.idleMu.Lock()
		if b.poolClosed || len(b.idle) == 0 {
			closed := b.poolClosed
			b.idleMu.Unlock()
			if closed {
				return nil, false, net.ErrClosed
			}
			connection, err := config.DialContext(ctx, "tcp", b.config.Address)
			return connection, false, err
		}
		last := len(b.idle) - 1
		entry := b.idle[last]
		b.idle[last] = idleUpstreamConnection{}
		b.idle = b.idle[:last]
		b.idleMu.Unlock()
		if now.Sub(entry.idleSince) >= b.config.IdleTimeout {
			_ = entry.connection.Close()
			continue
		}
		_ = entry.connection.SetDeadline(time.Time{})
		return entry.connection, true, nil
	}
}

func (b *proxyBackend) recycleConnection(connection net.Conn, now time.Time) bool {
	if connection == nil {
		return false
	}
	_ = connection.SetDeadline(time.Time{})
	b.idleMu.Lock()
	if b.poolClosed || len(b.idle) >= b.config.MaxIdleConnections {
		b.idleMu.Unlock()
		_ = connection.Close()
		return false
	}
	b.idle = append(b.idle, idleUpstreamConnection{connection: connection, idleSince: now})
	b.idleMu.Unlock()
	return true
}

func (b *proxyBackend) closeIdleConnections() error {
	b.idleMu.Lock()
	b.poolClosed = true
	idle := b.idle
	b.idle = nil
	b.idleMu.Unlock()
	var first error
	for _, entry := range idle {
		if err := entry.connection.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
