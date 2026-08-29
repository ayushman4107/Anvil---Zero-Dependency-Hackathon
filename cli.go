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
)

const (
	exitOK        = 0
	exitFailure   = 1
	exitUsage     = 2
	programName   = "anvil"
	plannedStatus = "scaffolded; implementation begins in later phases"
)

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootHelp(stdout)
		return exitOK
	}

	switch args[0] {
	case "help", "-h", "--help":
		printRootHelp(stdout)
		return exitOK
	case "dev-echo":
		return runDevEcho(args[1:], stdout, stderr)
	case "dev-http":
		return runDevHTTP(args[1:], stdout, stderr)
	case "dev-proxy":
		return runDevProxy(args[1:], stdout, stderr)
	case "proxy", "demo", "experiment", "bench":
		return runPlannedCommand(args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", programName, args[0])
		printRootHelp(stderr)
		return exitUsage
	}
}

func printRootHelp(w io.Writer) {
	fmt.Fprintln(w, `Anvil — an explainable reverse proxy and resilience-testing lab

Usage:
  anvil <command> [options]

Commands:
  proxy       Run the reverse proxy (planned)
  demo        Run the self-contained resilience demo (planned)
  experiment  Execute a deterministic failure experiment (planned)
  bench       Benchmark the Anvil data path (planned)
  dev-echo    Run the Phase 1 raw-TCP lifecycle proof
  dev-http    Run the Phase 3 raw HTTP server/router proof
  dev-proxy   Run the Phase 6 observable resilience-proxy proof
  help        Show this help

The current build includes the Phase 1 TCP lifecycle, Phase 2 strict HTTP/1.1
codec, Phase 3 network-facing server/router, Phase 4 bounded proxy transaction,
Phase 5 resilience core, and Phase 6 causal observability plane. Product commands
fail explicitly until their acceptance gates complete.`)
}

func runDevHTTP(args []string, stdout, stderr io.Writer) int {
	cfg := DefaultConfig()
	flags := flag.NewFlagSet("dev-http", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP address to listen on")
	flags.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "maximum concurrent connections")
	flags.IntVar(&cfg.MaxRequests, "max-requests-per-connection", cfg.MaxRequests, "maximum sequential requests per connection")
	flags.IntVar(&cfg.ReadTimeoutMS, "read-timeout-ms", cfg.ReadTimeoutMS, "per-read timeout in milliseconds")
	flags.IntVar(&cfg.WriteTimeoutMS, "write-timeout-ms", cfg.WriteTimeoutMS, "per-write timeout in milliseconds")
	flags.IntVar(&cfg.IdleTimeoutMS, "idle-timeout-ms", cfg.IdleTimeoutMS, "per-connection idle timeout in milliseconds")
	flags.IntVar(&cfg.ShutdownMS, "shutdown-timeout-ms", cfg.ShutdownMS, "graceful drain timeout in milliseconds")
	flags.IntVar(&cfg.ForceCloseMS, "force-close-timeout-ms", cfg.ForceCloseMS, "wait after force-closing connections in milliseconds")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s dev-http [options]\n", programName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s dev-http: unexpected arguments: %v\n", programName, flags.Args())
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "%s dev-http: invalid configuration: %v\n", programName, err)
		return exitUsage
	}

	router, err := developmentRouter()
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-http: routes: %v\n", programName, err)
		return exitFailure
	}
	handler, err := newHTTPConnectionHandler(router, cfg.httpServerConfig())
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-http: server configuration: %v\n", programName, err)
		return exitFailure
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-http: listen: %v\n", programName, err)
		return exitFailure
	}
	server, err := newTCPServer(listener, cfg.tcpServerConfig(), handler)
	if err != nil {
		_ = listener.Close()
		fmt.Fprintf(stderr, "%s dev-http: server: %v\n", programName, err)
		return exitFailure
	}

	fmt.Fprintf(stdout, "%s dev-http listening on %s\n", programName, listener.Addr())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := server.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "%s dev-http: %v\n", programName, err)
		return exitFailure
	}
	return exitOK
}

func developmentRouter() (*routeTree, error) {
	router := newRouteTree()
	routes := []struct {
		method  string
		pattern string
		handler routeHandler
	}{
		{method: "GET", pattern: "/health", handler: func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
			return textResponse(200, "ok\n"), nil
		}},
		{method: "GET", pattern: "/hello/:name", handler: func(_ context.Context, _ *httpRequest, params routeParams) (*httpResponse, error) {
			name, _ := params.Get("name")
			return textResponse(200, "hello "+name+"\n"), nil
		}},
		{method: "POST", pattern: "/echo", handler: func(_ context.Context, request *httpRequest, _ routeParams) (*httpResponse, error) {
			response := textResponse(200, string(request.Body))
			response.Headers = headerFields{{Name: "Content-Type", Value: "application/octet-stream"}}
			return response, nil
		}},
	}
	for _, route := range routes {
		if err := router.Register(route.method, route.pattern, route.handler); err != nil {
			return nil, err
		}
	}
	return router, nil
}

func runPlannedCommand(name string, args []string, stdout, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprintf(stdout, "Usage: %s %s [options]\n\nStatus: %s.\n", programName, name, plannedStatus)
		return exitOK
	}

	fmt.Fprintf(stderr, "%s %s: %s\n", programName, name, plannedStatus)
	return exitFailure
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func runDevEcho(args []string, stdout, stderr io.Writer) int {
	cfg := DefaultConfig()
	flags := flag.NewFlagSet("dev-echo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP address to listen on")
	flags.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "maximum concurrent connections")
	flags.IntVar(&cfg.ReadTimeoutMS, "read-timeout-ms", cfg.ReadTimeoutMS, "per-read timeout in milliseconds")
	flags.IntVar(&cfg.WriteTimeoutMS, "write-timeout-ms", cfg.WriteTimeoutMS, "per-write timeout in milliseconds")
	flags.IntVar(&cfg.IdleTimeoutMS, "idle-timeout-ms", cfg.IdleTimeoutMS, "per-connection idle timeout in milliseconds")
	flags.IntVar(&cfg.ShutdownMS, "shutdown-timeout-ms", cfg.ShutdownMS, "graceful drain timeout in milliseconds")
	flags.IntVar(&cfg.ForceCloseMS, "force-close-timeout-ms", cfg.ForceCloseMS, "wait after force-closing connections in milliseconds")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s dev-echo [options]\n", programName)
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s dev-echo: unexpected arguments: %v\n", programName, flags.Args())
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "%s dev-echo: invalid configuration: %v\n", programName, err)
		return exitUsage
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "%s dev-echo: listen: %v\n", programName, err)
		return exitFailure
	}

	fmt.Fprintf(stdout, "%s dev-echo listening on %s\n", programName, listener.Addr())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := serveEcho(ctx, listener, cfg); err != nil {
		fmt.Fprintf(stderr, "%s dev-echo: %v\n", programName, err)
		return exitFailure
	}
	return exitOK
}
