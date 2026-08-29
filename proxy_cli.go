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
	var upstreams backendFlagValues
	backendMaxInFlight := defaultBackendInFlight
	dialTimeoutMS := int(proxyCfg.DialTimeout / time.Millisecond)
	upstreamReadTimeoutMS := int(proxyCfg.ReadTimeout / time.Millisecond)
	upstreamWriteTimeoutMS := int(proxyCfg.WriteTimeout / time.Millisecond)

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
	flags.Var(&upstreams, "upstream", "backend in alias=host:port form; repeat for round robin")
	flags.IntVar(&backendMaxInFlight, "backend-max-in-flight", backendMaxInFlight, "maximum concurrent requests per backend")
	flags.IntVar(&dialTimeoutMS, "dial-timeout-ms", dialTimeoutMS, "upstream dial timeout in milliseconds")
	flags.IntVar(&upstreamReadTimeoutMS, "upstream-read-timeout-ms", upstreamReadTimeoutMS, "upstream response timeout in milliseconds")
	flags.IntVar(&upstreamWriteTimeoutMS, "upstream-write-timeout-ms", upstreamWriteTimeoutMS, "upstream request timeout in milliseconds")
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
	if backendMaxInFlight <= 0 || dialTimeoutMS <= 0 || upstreamReadTimeoutMS <= 0 || upstreamWriteTimeoutMS <= 0 {
		fmt.Fprintf(stderr, "%s dev-proxy: backend limits and upstream timeouts must be greater than zero\n", programName)
		return exitUsage
	}

	backends := make([]backendConfig, 0, len(upstreams))
	for _, value := range upstreams {
		separator := strings.IndexByte(value, '=')
		backends = append(backends, backendConfig{
			Alias:       value[:separator],
			Address:     value[separator+1:],
			MaxInFlight: backendMaxInFlight,
		})
	}
	pool, err := newBackendPool(backends)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: invalid upstream: %v\n", programName, err)
		return exitUsage
	}
	proxyCfg.DialTimeout = time.Duration(dialTimeoutMS) * time.Millisecond
	proxyCfg.ReadTimeout = time.Duration(upstreamReadTimeoutMS) * time.Millisecond
	proxyCfg.WriteTimeout = time.Duration(upstreamWriteTimeoutMS) * time.Millisecond
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

	fmt.Fprintf(stdout, "%s dev-proxy listening on %s with %d upstream(s)\n", programName, listener.Addr(), len(backends))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := server.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "%s dev-proxy: %v\n", programName, err)
		return exitFailure
	}
	return exitOK
}
