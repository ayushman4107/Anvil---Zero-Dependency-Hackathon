package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var errServerAlreadyStarted = errors.New("TCP server can be started only once")

type lifecycleError struct {
	Operation string
	Err       error
}

func (e *lifecycleError) Error() string {
	return fmt.Sprintf("TCP lifecycle %s: %v", e.Operation, e.Err)
}

func (e *lifecycleError) Unwrap() error {
	return e.Err
}

type tcpServerConfig struct {
	MaxConnections  int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	ForceCloseWait  time.Duration
}

func (c tcpServerConfig) validate() error {
	switch {
	case c.MaxConnections <= 0:
		return fmt.Errorf("max connections must be greater than zero")
	case c.ReadTimeout <= 0:
		return fmt.Errorf("read timeout must be greater than zero")
	case c.WriteTimeout <= 0:
		return fmt.Errorf("write timeout must be greater than zero")
	case c.IdleTimeout <= 0:
		return fmt.Errorf("idle timeout must be greater than zero")
	case c.ShutdownTimeout <= 0:
		return fmt.Errorf("shutdown timeout must be greater than zero")
	case c.ForceCloseWait <= 0:
		return fmt.Errorf("force-close wait must be greater than zero")
	default:
		return nil
	}
}

type connectionHandler func(context.Context, *clientConn) error

const (
	serverStateNew int32 = iota
	serverStateServing
	serverStateDraining
	serverStateClosed
)

type serverStats struct {
	State         string
	Accepted      uint64
	Admitted      uint64
	Rejected      uint64
	Active        int64
	PeakActive    int64
	Completed     uint64
	HandlerErrors uint64
	ForcedClosed  uint64
}

type tcpServer struct {
	listener net.Listener
	config   tcpServerConfig
	handler  connectionHandler

	state              atomic.Int32
	connectionSequence atomic.Uint64
	accepted           atomic.Uint64
	admitted           atomic.Uint64
	rejected           atomic.Uint64
	activeCount        atomic.Int64
	peakActive         atomic.Int64
	completed          atomic.Uint64
	handlerErrors      atomic.Uint64
	forcedClosed       atomic.Uint64

	admission chan struct{}
	workers   sync.WaitGroup
	activeMu  sync.Mutex
	active    map[*clientConn]struct{}

	handlerContext context.Context
	cancelHandlers context.CancelFunc
	closeListener  sync.Once
}

func newTCPServer(listener net.Listener, config tcpServerConfig, handler connectionHandler) (*tcpServer, error) {
	if listener == nil {
		return nil, fmt.Errorf("listener is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("connection handler is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}

	handlerContext, cancelHandlers := context.WithCancel(context.Background())
	return &tcpServer{
		listener:       listener,
		config:         config,
		handler:        handler,
		admission:      make(chan struct{}, config.MaxConnections),
		active:         make(map[*clientConn]struct{}, config.MaxConnections),
		handlerContext: handlerContext,
		cancelHandlers: cancelHandlers,
	}, nil
}

func (s *tcpServer) Serve(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("serve context is required")
	}
	if !s.state.CompareAndSwap(serverStateNew, serverStateServing) {
		return errServerAlreadyStarted
	}

	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.beginShutdown()
		case <-watcherDone:
		}
	}()

	var acceptErr error
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.state.Load() == serverStateServing {
				acceptErr = &lifecycleError{Operation: "accept", Err: err}
				s.beginShutdown()
			}
			break
		}

		s.accepted.Add(1)
		if s.state.Load() != serverStateServing || !s.admit(conn) {
			s.rejected.Add(1)
			_ = conn.Close()
			continue
		}
	}

	close(watcherDone)
	drainErr := s.drain()
	s.state.Store(serverStateClosed)
	s.cancelHandlers()

	if acceptErr != nil {
		return acceptErr
	}
	return drainErr
}

func (s *tcpServer) admit(conn net.Conn) bool {
	select {
	case s.admission <- struct{}{}:
	default:
		return false
	}

	managed := &clientConn{
		Conn:         conn,
		id:           s.connectionSequence.Add(1),
		acceptedAt:   time.Now(),
		lastActivity: time.Now(),
		readTimeout:  s.config.ReadTimeout,
		writeTimeout: s.config.WriteTimeout,
		idleTimeout:  s.config.IdleTimeout,
	}

	s.activeMu.Lock()
	s.active[managed] = struct{}{}
	s.activeMu.Unlock()

	s.admitted.Add(1)
	active := s.activeCount.Add(1)
	s.recordPeak(active)
	s.workers.Add(1)
	go s.runConnection(managed)
	return true
}

func (s *tcpServer) runConnection(conn *clientConn) {
	defer s.workers.Done()
	defer func() { <-s.admission }()
	defer s.completed.Add(1)
	defer s.activeCount.Add(-1)
	defer func() {
		s.activeMu.Lock()
		delete(s.active, conn)
		s.activeMu.Unlock()
		_ = conn.Close()
	}()

	if err := s.callHandler(conn); err != nil && s.handlerContext.Err() == nil {
		s.handlerErrors.Add(1)
	}
}

func (s *tcpServer) callHandler(conn *clientConn) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("connection handler panic: %v", recovered)
		}
	}()
	return s.handler(s.handlerContext, conn)
}

func (s *tcpServer) recordPeak(active int64) {
	for {
		peak := s.peakActive.Load()
		if active <= peak || s.peakActive.CompareAndSwap(peak, active) {
			return
		}
	}
}

func (s *tcpServer) beginShutdown() {
	if s.state.CompareAndSwap(serverStateServing, serverStateDraining) {
		s.closeListener.Do(func() { _ = s.listener.Close() })
	}
}

func (s *tcpServer) drain() error {
	workersDone := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(workersDone)
	}()

	grace := time.NewTimer(s.config.ShutdownTimeout)
	defer grace.Stop()
	select {
	case <-workersDone:
		return nil
	case <-grace.C:
	}

	s.cancelHandlers()
	s.forceCloseActive()
	forceWait := time.NewTimer(s.config.ForceCloseWait)
	defer forceWait.Stop()
	select {
	case <-workersDone:
		return nil
	case <-forceWait.C:
		return &lifecycleError{Operation: "shutdown", Err: context.DeadlineExceeded}
	}
}

func (s *tcpServer) forceCloseActive() {
	s.activeMu.Lock()
	connections := make([]*clientConn, 0, len(s.active))
	for conn := range s.active {
		connections = append(connections, conn)
	}
	s.activeMu.Unlock()

	s.forcedClosed.Add(uint64(len(connections)))
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (s *tcpServer) Stats() serverStats {
	return serverStats{
		State:         serverStateName(s.state.Load()),
		Accepted:      s.accepted.Load(),
		Admitted:      s.admitted.Load(),
		Rejected:      s.rejected.Load(),
		Active:        s.activeCount.Load(),
		PeakActive:    s.peakActive.Load(),
		Completed:     s.completed.Load(),
		HandlerErrors: s.handlerErrors.Load(),
		ForcedClosed:  s.forcedClosed.Load(),
	}
}

func serverStateName(state int32) string {
	switch state {
	case serverStateNew:
		return "new"
	case serverStateServing:
		return "serving"
	case serverStateDraining:
		return "draining"
	case serverStateClosed:
		return "closed"
	default:
		return "invalid"
	}
}

type clientConn struct {
	net.Conn
	id           uint64
	acceptedAt   time.Time
	lastActivity time.Time
	readTimeout  time.Duration
	writeTimeout time.Duration
	idleTimeout  time.Duration
}

func (c *clientConn) ID() uint64 {
	return c.id
}

func (c *clientConn) AcceptedAt() time.Time {
	return c.acceptedAt
}

func (c *clientConn) Read(buffer []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(c.deadline(c.readTimeout)); err != nil {
		return 0, err
	}
	read, err := c.Conn.Read(buffer)
	if read > 0 {
		c.lastActivity = time.Now()
	}
	return read, err
}

func (c *clientConn) Write(buffer []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(c.deadline(c.writeTimeout)); err != nil {
		return 0, err
	}
	written, err := c.Conn.Write(buffer)
	if written > 0 {
		c.lastActivity = time.Now()
	}
	return written, err
}

func (c *clientConn) deadline(operationTimeout time.Duration) time.Time {
	now := time.Now()
	operationDeadline := now.Add(operationTimeout)
	idleDeadline := c.lastActivity.Add(c.idleTimeout)
	if idleDeadline.Before(operationDeadline) {
		return idleDeadline
	}
	return operationDeadline
}
