package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"
)

type backendFlagValues []string

func (v *backendFlagValues) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(*v, ",")
}

func (v *backendFlagValues) Set(value string) error {
	if len(*v) >= maxBackendPoolSize {
		return fmt.Errorf("at most %d upstreams are allowed", maxBackendPoolSize)
	}
	separator := strings.IndexByte(value, '=')
	if separator <= 0 || separator == len(value)-1 {
		return fmt.Errorf("upstream must use alias=host:port")
	}
	*v = append(*v, value)
	return nil
}

func runDevProxy(args []string, stdout, stderr io.Writer) int {
	cfg := DefaultConfig()
	proxyCfg := defaultProxyConfig()
	resilienceCfg := defaultResilienceConfig()
	healthCfg := defaultActiveHealthConfig()
	observabilityCfg := defaultObservabilityConfig()
	adminCfg := defaultAdminConfig()
	var upstreams backendFlagValues
	adminListen := defaultAdminListen
	backendMaxInFlight := defaultBackendInFlight
	backendMaxIdle := defaultBackendIdleConnections
	backendIdleTimeoutMS := int(defaultBackendIdleTimeout / time.Millisecond)
	selector := string(resilienceCfg.Selector)
	healthChecks := false
	healthPath := "/health"
	dialTimeoutMS := int(proxyCfg.DialTimeout / time.Millisecond)
	upstreamReadTimeoutMS := int(proxyCfg.ReadTimeout / time.Millisecond)
	upstreamWriteTimeoutMS := int(proxyCfg.WriteTimeout / time.Millisecond)
	retryTimeoutMS := int(proxyCfg.RetryTimeout / time.Millisecond)
	circuitCooldownMS := int(resilienceCfg.CircuitCooldown / time.Millisecond)
	passiveIntervalMS := int(resilienceCfg.PassiveInterval / time.Millisecond)
	healthIntervalMS := int(healthCfg.Interval / time.Millisecond)
	healthTimeoutMS := int(healthCfg.Timeout / time.Millisecond)
	sseHeartbeatMS := int(adminCfg.Heartbeat / time.Millisecond)

	flags := flag.NewFlagSet("dev-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP address to listen on")
	flags.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "maximum concurrent downstream connections")
	flags.IntVar(&cfg.MaxRequests, "max-requests-per-connection", cfg.MaxRequests, "maximum sequential downstream requests per connection")
	flags.IntVar(&cfg.ReadTimeoutMS, "read-timeout-ms", cfg.ReadTimeoutMS, "downstream per-read timeout in milliseconds")
	flags.IntVar(&cfg.WriteTimeoutMS, "write-timeout-ms", cfg.WriteTimeoutMS, "downstream per-write timeout in milliseconds")
	flags.IntVar(&cfg.IdleTimeoutMS, "idle-timeout-ms", cfg.IdleTimeoutMS, "downstream idle timeout in milliseconds")
	flags.IntVar(&cfg.ShutdownMS, "shutdown-timeout-ms", cfg.ShutdownMS, "graceful drain timeout in milliseconds")
	flags.IntVar(&cfg.ForceCloseMS, "force-close-timeout-ms", cfg.ForceCloseMS, "wait after force-closing downstream connections in milliseconds")
	flags.Var(&upstreams, "upstream", "backend in alias=host:port form; repeat to build a pool")
	flags.StringVar(&selector, "selector", selector, "backend selector: round-robin or least-in-flight")
	flags.IntVar(&backendMaxInFlight, "backend-max-in-flight", backendMaxInFlight, "maximum concurrent requests per backend")
	flags.IntVar(&backendMaxIdle, "backend-max-idle", backendMaxIdle, "maximum reusable idle connections per backend")
	flags.IntVar(&backendIdleTimeoutMS, "backend-idle-timeout-ms", backendIdleTimeoutMS, "maximum upstream idle connection age in milliseconds")
	flags.IntVar(&dialTimeoutMS, "dial-timeout-ms", dialTimeoutMS, "upstream dial timeout in milliseconds")
	flags.IntVar(&upstreamReadTimeoutMS, "upstream-read-timeout-ms", upstreamReadTimeoutMS, "upstream response timeout in milliseconds")
	flags.IntVar(&upstreamWriteTimeoutMS, "upstream-write-timeout-ms", upstreamWriteTimeoutMS, "upstream request timeout in milliseconds")
	flags.IntVar(&proxyCfg.MaxAttempts, "max-attempts", proxyCfg.MaxAttempts, "maximum total attempts for replayable GET and HEAD requests")
	flags.IntVar(&retryTimeoutMS, "retry-timeout-ms", retryTimeoutMS, "maximum total safe-retry window in milliseconds")
	flags.IntVar(&resilienceCfg.PassiveFailureThreshold, "circuit-failures", resilienceCfg.PassiveFailureThreshold, "passive failures within the interval before opening a circuit")
	flags.IntVar(&passiveIntervalMS, "circuit-interval-ms", passiveIntervalMS, "passive failure counting interval in milliseconds")
	flags.IntVar(&circuitCooldownMS, "circuit-cooldown-ms", circuitCooldownMS, "open-circuit cooldown before a bounded half-open probe")
	flags.IntVar(&resilienceCfg.HalfOpenMaxRequests, "half-open-max", resilienceCfg.HalfOpenMaxRequests, "maximum concurrent half-open probes")
	flags.IntVar(&resilienceCfg.HalfOpenSuccesses, "half-open-successes", resilienceCfg.HalfOpenSuccesses, "successful half-open probes required to close")
	flags.BoolVar(&healthChecks, "health-checks", healthChecks, "enable active HTTP health checks")
	flags.StringVar(&healthPath, "health-path", healthPath, "origin-form active health-check path")
	flags.IntVar(&healthIntervalMS, "health-interval-ms", healthIntervalMS, "active health interval in milliseconds")
	flags.IntVar(&healthTimeoutMS, "health-timeout-ms", healthTimeoutMS, "active health request timeout in milliseconds")
	flags.IntVar(&resilienceCfg.ActiveFailureThreshold, "health-failures", resilienceCfg.ActiveFailureThreshold, "active failures required to mark unhealthy")
	flags.IntVar(&resilienceCfg.ActiveSuccessThreshold, "health-successes", resilienceCfg.ActiveSuccessThreshold, "active successes required to recover")
	flags.StringVar(&adminListen, "admin-listen", adminListen, "loopback administration and dashboard address")
	flags.IntVar(&observabilityCfg.LedgerCapacity, "ledger-capacity", observabilityCfg.LedgerCapacity, "maximum retained decision events")
	flags.IntVar(&observabilityCfg.MaxSubscribers, "max-subscribers", observabilityCfg.MaxSubscribers, "maximum concurrent SSE subscribers")
	flags.IntVar(&observabilityCfg.SubscriberQueue, "subscriber-queue", observabilityCfg.SubscriberQueue, "bounded pending events per SSE subscriber")
	flags.IntVar(&sseHeartbeatMS, "sse-heartbeat-ms", sseHeartbeatMS, "SSE heartbeat interval in milliseconds")
	flags.BoolVar(&proxyCfg.AddForwarded, "forwarded", proxyCfg.AddForwarded, "regenerate the RFC Forwarded field from the immediate peer")
	flags.BoolVar(&proxyCfg.AddXForwarded, "x-forwarded", proxyCfg.AddXForwarded, "regenerate compatibility X-Forwarded fields")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s dev-proxy --upstream alias=host:port [--upstream alias=host:port] [options]\n", programName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s dev-proxy: unexpected arguments: %v\n", programName, flags.Args())
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: invalid configuration: %v\n", programName, err)
		return exitUsage
	}
	if len(upstreams) == 0 {
		fmt.Fprintf(stderr, "%s dev-proxy: at least one --upstream is required\n", programName)
		return exitUsage
	}
	if err := validateAdminListen(adminListen); err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: invalid admin listener: %v\n", programName, err)
		return exitUsage
	}
	if len(upstreams) > maxBackendPoolSize || backendMaxInFlight <= 0 || backendMaxInFlight > maxBackendInFlight || backendMaxIdle <= 0 || backendMaxIdle > maxBackendIdleConnections || !validMilliseconds(backendIdleTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(dialTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(upstreamReadTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(upstreamWriteTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(retryTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(passiveIntervalMS, maxConfiguredTimeout) || !validMilliseconds(circuitCooldownMS, maxConfiguredTimeout) || !validMilliseconds(healthIntervalMS, maxConfiguredTimeout) || !validMilliseconds(healthTimeoutMS, maxConfiguredTimeout) || !validMilliseconds(sseHeartbeatMS, maxConfiguredTimeout/4) || observabilityCfg.LedgerCapacity <= 0 || observabilityCfg.LedgerCapacity > maxLedgerCapacity || observabilityCfg.MaxSubscribers <= 0 || observabilityCfg.MaxSubscribers > maxSSESubscribers || observabilityCfg.SubscriberQueue <= 0 || observabilityCfg.SubscriberQueue > maxSubscriberQueue || observabilityCfg.MaxSubscribers > maxQueuedSSEEvents/observabilityCfg.SubscriberQueue {
		fmt.Fprintf(stderr, "%s dev-proxy: backend limits and upstream timeouts must be greater than zero\n", programName)
		return exitUsage
	}

	backends := make([]backendConfig, 0, len(upstreams))
	for _, value := range upstreams {
		separator := strings.IndexByte(value, '=')
		backends = append(backends, backendConfig{
			Alias:              value[:separator],
			Address:            value[separator+1:],
			MaxInFlight:        backendMaxInFlight,
			MaxIdleConnections: backendMaxIdle,
			IdleTimeout:        time.Duration(backendIdleTimeoutMS) * time.Millisecond,
			HealthPath:         healthPath,
		})
	}
	resilienceCfg.Selector = selectorPolicy(selector)
	resilienceCfg.PassiveInterval = time.Duration(passiveIntervalMS) * time.Millisecond
	resilienceCfg.CircuitCooldown = time.Duration(circuitCooldownMS) * time.Millisecond
	var observer *observability
	previousCircuitCallback := resilienceCfg.OnCircuitTransition
	resilienceCfg.OnCircuitTransition = func(transition circuitTransition) {
		if previousCircuitCallback != nil {
			previousCircuitCallback(transition)
		}
		if observer != nil {
			observer.recordCircuitTransition(transition)
		}
	}
	previousHealthCallback := resilienceCfg.OnHealthTransition
	resilienceCfg.OnHealthTransition = func(transition healthTransition) {
		if previousHealthCallback != nil {
			previousHealthCallback(transition)
		}
		if observer != nil {
			observer.recordHealthTransition(transition)
		}
	}
	pool, err := newBackendPoolWithConfig(backends, resilienceCfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: invalid upstream: %v\n", programName, err)
		return exitUsage
	}
	proxyCfg.DialTimeout = time.Duration(dialTimeoutMS) * time.Millisecond
	proxyCfg.ReadTimeout = time.Duration(upstreamReadTimeoutMS) * time.Millisecond
	proxyCfg.WriteTimeout = time.Duration(upstreamWriteTimeoutMS) * time.Millisecond
	proxyCfg.RetryTimeout = time.Duration(retryTimeoutMS) * time.Millisecond
	defer pool.Close()
	observer, err = newObservability(observabilityCfg, pool)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: observability configuration: %v\n", programName, err)
		return exitUsage
	}
	defer observer.close()
	proxyCfg.Observability = observer
	proxyHandler, err := newProxyHandler(pool, proxyCfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: proxy configuration: %v\n", programName, err)
		return exitUsage
	}
	router := newRouteTree()
	if err := router.Register(anyMethod, "/", proxyHandler); err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: root route: %v\n", programName, err)
		return exitFailure
	}
	if err := router.Register(anyMethod, "/*path", proxyHandler); err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: wildcard route: %v\n", programName, err)
		return exitFailure
	}
	handler, err := newHTTPConnectionHandler(router, cfg.httpServerConfig())
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: HTTP server configuration: %v\n", programName, err)
		return exitFailure
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: listen: %v\n", programName, err)
		return exitFailure
	}
	server, err := newTCPServer(listener, cfg.tcpServerConfig(), handler)
	if err != nil {
		_ = listener.Close()
		fmt.Fprintf(stderr, "%s dev-proxy: server: %v\n", programName, err)
		return exitFailure
	}
	adminCfg.Heartbeat = time.Duration(sseHeartbeatMS) * time.Millisecond
	minimumAdminIdle := 3 * adminCfg.Heartbeat
	if adminCfg.TCP.IdleTimeout <= minimumAdminIdle {
		adminCfg.TCP.IdleTimeout = minimumAdminIdle + time.Second
	}
	adminListener, err := net.Listen("tcp", adminListen)
	if err != nil {
		_ = listener.Close()
		fmt.Fprintf(stderr, "%s dev-proxy: admin listen: %v\n", programName, err)
		return exitFailure
	}
	adminServer, err := newAdminServer(adminListener, adminCfg, observer)
	if err != nil {
		_ = adminListener.Close()
		_ = listener.Close()
		fmt.Fprintf(stderr, "%s dev-proxy: admin server: %v\n", programName, err)
		return exitFailure
	}
	observer.setServers(server, adminServer)

	fmt.Fprintf(stdout, "%s dev-proxy listening on %s with %d upstream(s); dashboard http://%s/\n", programName, listener.Addr(), len(backends), adminListener.Addr())
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(signalContext)
	defer cancel()
	var healthChecker *activeHealthChecker
	if healthChecks {
		healthCfg.Interval = time.Duration(healthIntervalMS) * time.Millisecond
		healthCfg.Timeout = time.Duration(healthTimeoutMS) * time.Millisecond
		healthChecker, err = newActiveHealthChecker(pool, proxyCfg, healthCfg)
		if err != nil {
			fmt.Fprintf(stderr, "%s dev-proxy: health configuration: %v\n", programName, err)
			return exitUsage
		}
		if err := healthChecker.Start(ctx); err != nil {
			fmt.Fprintf(stderr, "%s dev-proxy: health start: %v\n", programName, err)
			return exitFailure
		}
		defer healthChecker.Stop()
	}
	type serverResult struct {
		name string
		err  error
	}
	results := make(chan serverResult, 2)
	go func() { results <- serverResult{name: "proxy", err: server.Serve(ctx)} }()
	go func() { results <- serverResult{name: "admin", err: adminServer.Serve(ctx)} }()
	first := <-results
	unexpectedStop := signalContext.Err() == nil
	cancel()
	second := <-results
	if first.err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: %s server: %v\n", programName, first.name, first.err)
		return exitFailure
	}
	if second.err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: %s server: %v\n", programName, second.name, second.err)
		return exitFailure
	}
	if unexpectedStop {
		fmt.Fprintf(stderr, "%s dev-proxy: %s server stopped unexpectedly\n", programName, first.name)
		return exitFailure
	}
	return exitOK
}
