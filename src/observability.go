package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultLedgerCapacity  = 2_048
	defaultMaxSubscribers  = 32
	defaultSubscriberQueue = 64
	maxLedgerCapacity      = 1_000_000
	maxSSESubscribers      = 1_024
	maxSubscriberQueue     = 4_096
	maxQueuedSSEEvents     = 1_000_000
)

type eventType string

const (
	eventRequestStarted    eventType = "request_started"
	eventBackendSelected   eventType = "backend_selected"
	eventAttemptFailed     eventType = "attempt_failed"
	eventRetryScheduled    eventType = "retry_scheduled"
	eventRequestCompleted  eventType = "request_completed"
	eventCircuitTransition eventType = "circuit_transition"
	eventHealthTransition  eventType = "health_transition"
	eventFixtureTransition eventType = "fixture_transition"
)

type decisionEvent struct {
	Sequence       uint64    `json:"sequence"`
	ElapsedMicros  int64     `json:"elapsed_micros"`
	Type           eventType `json:"type"`
	RequestID      string    `json:"request_id,omitempty"`
	RouteAlias     string    `json:"route_alias,omitempty"`
	BackendAlias   string    `json:"backend_alias,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	PreviousState  string    `json:"previous_state,omitempty"`
	NewState       string    `json:"new_state,omitempty"`
	Attempt        int       `json:"attempt,omitempty"`
	Status         int       `json:"status,omitempty"`
	DurationMicros int64     `json:"duration_micros,omitempty"`
}

type ledgerSnapshot struct {
	Capacity       int             `json:"capacity"`
	Count          int             `json:"count"`
	OldestSequence uint64          `json:"oldest_sequence"`
	LatestSequence uint64          `json:"latest_sequence"`
	Gap            bool            `json:"gap"`
	Events         []decisionEvent `json:"events"`
}

type eventLedger struct {
	mu       sync.Mutex
	entries  []decisionEvent
	start    int
	count    int
	sequence uint64
}

func newEventLedger(capacity int) (*eventLedger, error) {
	if capacity <= 0 || capacity > maxLedgerCapacity {
		return nil, fmt.Errorf("ledger capacity must be between 1 and %d", maxLedgerCapacity)
	}
	return &eventLedger{entries: make([]decisionEvent, capacity)}, nil
}

func (l *eventLedger) append(event decisionEvent, elapsed time.Duration) decisionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	event.Sequence = l.sequence
	event.ElapsedMicros = max(elapsed.Microseconds(), 0)
	if l.count < len(l.entries) {
		index := (l.start + l.count) % len(l.entries)
		l.entries[index] = event
		l.count++
	} else {
		l.entries[l.start] = event
		l.start = (l.start + 1) % len(l.entries)
	}
	return event
}

func (l *eventLedger) snapshotSince(after uint64) ledgerSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot := ledgerSnapshot{Capacity: len(l.entries), Count: l.count, Events: make([]decisionEvent, 0, l.count)}
	if l.count == 0 {
		return snapshot
	}
	snapshot.OldestSequence = l.entries[l.start].Sequence
	latestIndex := (l.start + l.count - 1) % len(l.entries)
	snapshot.LatestSequence = l.entries[latestIndex].Sequence
	futureCursor := after > snapshot.LatestSequence
	snapshot.Gap = futureCursor || (after != 0 && snapshot.OldestSequence > 1 && after < snapshot.OldestSequence-1)
	effectiveAfter := after
	if futureCursor {
		effectiveAfter = snapshot.OldestSequence - 1
	}
	for offset := range l.count {
		event := l.entries[(l.start+offset)%len(l.entries)]
		if event.Sequence > effectiveAfter {
			snapshot.Events = append(snapshot.Events, event)
		}
	}
	return snapshot
}

var errSubscriberLimit = errors.New("SSE subscriber limit reached")

type eventSubscription struct {
	ID     uint64
	Events <-chan decisionEvent
	Done   <-chan struct{}
}

type sseSubscriber struct {
	events chan decisionEvent
	done   chan struct{}
}

type sseHub struct {
	mu             sync.RWMutex
	subscribers    map[uint64]sseSubscriber
	nextSubscriber uint64
	maxSubscribers int
	queueCapacity  int
	dropped        atomic.Uint64
	closed         bool
}

func newSSEHub(maxSubscribers, queueCapacity int) (*sseHub, error) {
	if maxSubscribers <= 0 || maxSubscribers > maxSSESubscribers || queueCapacity <= 0 || queueCapacity > maxSubscriberQueue || maxSubscribers > maxQueuedSSEEvents/queueCapacity {
		return nil, fmt.Errorf("SSE subscriber and queue bounds exceed the %d-event aggregate limit", maxQueuedSSEEvents)
	}
	return &sseHub{
		subscribers:    make(map[uint64]sseSubscriber, maxSubscribers),
		maxSubscribers: maxSubscribers,
		queueCapacity:  queueCapacity,
	}, nil
}

func (h *sseHub) subscribe() (eventSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return eventSubscription{}, netClosedError("SSE hub")
	}
	if len(h.subscribers) >= h.maxSubscribers {
		return eventSubscription{}, errSubscriberLimit
	}
	h.nextSubscriber++
	subscriber := sseSubscriber{events: make(chan decisionEvent, h.queueCapacity), done: make(chan struct{})}
	h.subscribers[h.nextSubscriber] = subscriber
	return eventSubscription{ID: h.nextSubscriber, Events: subscriber.events, Done: subscriber.done}, nil
}

func (h *sseHub) unsubscribe(id uint64) {
	h.mu.Lock()
	subscriber, exists := h.subscribers[id]
	delete(h.subscribers, id)
	h.mu.Unlock()
	if exists {
		close(subscriber.done)
	}
}

func (h *sseHub) publish(event decisionEvent) {
	h.mu.RLock()
	queues := make([]chan decisionEvent, 0, len(h.subscribers))
	for _, subscriber := range h.subscribers {
		queues = append(queues, subscriber.events)
	}
	h.mu.RUnlock()
	for _, queue := range queues {
		select {
		case queue <- event:
		default:
			h.dropped.Add(1)
		}
	}
}

func (h *sseHub) stats() (subscribers int, dropped uint64) {
	h.mu.RLock()
	subscribers = len(h.subscribers)
	h.mu.RUnlock()
	return subscribers, h.dropped.Load()
}

func (h *sseHub) close() {
	h.mu.Lock()
	done := make([]chan struct{}, 0, len(h.subscribers))
	if !h.closed {
		h.closed = true
		for id, subscriber := range h.subscribers {
			delete(h.subscribers, id)
			done = append(done, subscriber.done)
		}
	}
	h.mu.Unlock()
	for _, channel := range done {
		close(channel)
	}
}

type observabilityConfig struct {
	LedgerCapacity  int
	MaxSubscribers  int
	SubscriberQueue int
	Now             func() time.Time
}

func defaultObservabilityConfig() observabilityConfig {
	return observabilityConfig{
		LedgerCapacity:  defaultLedgerCapacity,
		MaxSubscribers:  defaultMaxSubscribers,
		SubscriberQueue: defaultSubscriberQueue,
		Now:             time.Now,
	}
}

type observability struct {
	startedAt    time.Time
	now          func() time.Time
	ledger       *eventLedger
	hub          *sseHub
	metrics      *proxyMetrics
	pool         *backendPool
	deliveryMu   sync.Mutex
	deliveryCond *sync.Cond
	nextDelivery uint64

	serversMu   sync.RWMutex
	proxyServer *tcpServer
	adminServer *tcpServer
}

func newObservability(config observabilityConfig, pool *backendPool) (*observability, error) {
	if config.Now == nil {
		return nil, fmt.Errorf("observability clock is required")
	}
	ledger, err := newEventLedger(config.LedgerCapacity)
	if err != nil {
		return nil, err
	}
	hub, err := newSSEHub(config.MaxSubscribers, config.SubscriberQueue)
	if err != nil {
		return nil, err
	}
	observability := &observability{
		startedAt:    config.Now(),
		now:          config.Now,
		ledger:       ledger,
		hub:          hub,
		metrics:      newProxyMetrics(),
		pool:         pool,
		nextDelivery: 1,
	}
	observability.deliveryCond = sync.NewCond(&observability.deliveryMu)
	return observability, nil
}

func (o *observability) publish(event decisionEvent) decisionEvent {
	if o == nil {
		return event
	}
	event = o.ledger.append(event, o.now().Sub(o.startedAt))
	o.deliveryMu.Lock()
	for event.Sequence != o.nextDelivery {
		o.deliveryCond.Wait()
	}
	o.deliveryMu.Unlock()
	o.hub.publish(event)
	o.deliveryMu.Lock()
	o.nextDelivery++
	o.deliveryCond.Broadcast()
	o.deliveryMu.Unlock()
	return event
}

func (o *observability) close() {
	if o != nil {
		o.hub.close()
	}
}

func (o *observability) setServers(proxyServer, adminServer *tcpServer) {
	if o == nil {
		return
	}
	o.serversMu.Lock()
	o.proxyServer = proxyServer
	o.adminServer = adminServer
	o.serversMu.Unlock()
}

func (o *observability) serverSnapshots() (serverStats, serverStats) {
	if o == nil {
		return serverStats{}, serverStats{}
	}
	o.serversMu.RLock()
	proxyServer, adminServer := o.proxyServer, o.adminServer
	o.serversMu.RUnlock()
	var proxyStats, adminStats serverStats
	if proxyServer != nil {
		proxyStats = proxyServer.Stats()
	}
	if adminServer != nil {
		adminStats = adminServer.Stats()
	}
	return proxyStats, adminStats
}

func (o *observability) recordCircuitTransition(transition circuitTransition) {
	if o == nil {
		return
	}
	o.metrics.circuitTransitions.Add(1)
	o.publish(decisionEvent{
		Type:          eventCircuitTransition,
		BackendAlias:  transition.BackendAlias,
		Reason:        transition.Reason,
		PreviousState: string(transition.From),
		NewState:      string(transition.To),
	})
}

type healthTransition struct {
	BackendAlias string
	FromHealthy  bool
	ToHealthy    bool
	At           time.Time
	Reason       string
}

func (o *observability) recordHealthTransition(transition healthTransition) {
	if o == nil {
		return
	}
	o.metrics.healthTransitions.Add(1)
	o.publish(decisionEvent{
		Type:          eventHealthTransition,
		BackendAlias:  transition.BackendAlias,
		Reason:        transition.Reason,
		PreviousState: healthStateName(transition.FromHealthy),
		NewState:      healthStateName(transition.ToHealthy),
	})
}

func healthStateName(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}

func netClosedError(component string) error {
	return fmt.Errorf("%s is closed", component)
}
