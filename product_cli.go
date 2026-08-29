package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

func runDemo(args []string, stdout, stderr io.Writer) int {
	value := defaultDemoScenario()
	originalDuration := value.DurationMS
	jsonOnly := false
	jsonOutput := ""
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&value.DurationMS, "duration-ms", value.DurationMS, "bounded demo duration in milliseconds")
	flags.IntVar(&value.Load.Requests, "requests", value.Load.Requests, "maximum generated requests")
	flags.IntVar(&value.Load.RatePerSecond, "rate-per-second", value.Load.RatePerSecond, "paced requests per second; zero is unpaced")
	flags.BoolVar(&jsonOnly, "json", false, "write the JSON receipt to stdout")
	flags.StringVar(&jsonOutput, "json-out", "", "optional path for the JSON receipt")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s demo [options]\n", programName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s demo: unexpected arguments: %v\n", programName, flags.Args())
		return exitUsage
	}
	if value.DurationMS != originalDuration && value.DurationMS > 0 {
		for index := range value.Steps {
			value.Steps[index].AtMS = max(value.Steps[index].AtMS*value.DurationMS/originalDuration, 1)
		}
	}
	value.LedgerCapacity = max(value.LedgerCapacity, value.requiredLedgerCapacity())
	return runScenarioCommand(value, jsonOnly, jsonOutput, stdout, stderr)
}

func runExperimentCommand(args []string, stdout, stderr io.Writer) int {
	scenarioPath := ""
	jsonOnly := false
	jsonOutput := ""
	flags := flag.NewFlagSet("experiment", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&scenarioPath, "scenario", "", "strict JSON scenario file")
	flags.BoolVar(&jsonOnly, "json", false, "write the JSON receipt to stdout")
	flags.StringVar(&jsonOutput, "json-out", "", "optional path for the JSON receipt")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s experiment --scenario FILE [options]\n", programName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 || scenarioPath == "" {
		fmt.Fprintf(stderr, "%s experiment: --scenario is required and positional arguments are not accepted\n", programName)
		return exitUsage
	}
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s experiment: read scenario: %v\n", programName, err)
		return exitFailure
	}
	value, err := parseScenario(data)
	if err != nil {
		fmt.Fprintf(stderr, "%s experiment: invalid scenario: %v\n", programName, err)
		return exitUsage
	}
	return runScenarioCommand(value, jsonOnly, jsonOutput, stdout, stderr)
}

func runScenarioCommand(value scenario, jsonOnly bool, jsonOutput string, stdout, stderr io.Writer) int {
	lab, err := newExperimentLab(value)
	if err != nil {
		fmt.Fprintf(stderr, "%s experiment: initialize: %v\n", programName, err)
		return exitUsage
	}
	defer lab.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	progress := stdout
	if jsonOnly {
		progress = io.Discard
	}
	receipt, err := lab.run(ctx, progress)
	if err != nil {
		fmt.Fprintf(stderr, "%s experiment: %v\n", programName, err)
		return exitFailure
	}
	encoded, err := marshalReceipt(receipt)
	if err != nil {
		fmt.Fprintf(stderr, "%s experiment: encode receipt: %v\n", programName, err)
		return exitFailure
	}
	if jsonOutput != "" {
		if err := os.WriteFile(jsonOutput, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "%s experiment: write receipt: %v\n", programName, err)
			return exitFailure
		}
	}
	if jsonOnly {
		fmt.Fprintln(stdout, string(encoded))
	} else if err := writeHumanReceipt(stdout, receipt); err != nil {
		fmt.Fprintf(stderr, "%s experiment: write receipt: %v\n", programName, err)
		return exitFailure
	}
	if !receipt.Passed {
		return exitFailure
	}
	return exitOK
}

func runBenchCommand(args []string, stdout, stderr io.Writer) int {
	config := benchmarkConfig{Method: "GET", Path: "/", Workers: 4, MaxRequests: 1_000, Duration: 10 * time.Second, Timeout: 2 * time.Second, Limits: defaultHTTPLimits()}
	durationMS := int(config.Duration / time.Millisecond)
	timeoutMS := int(config.Timeout / time.Millisecond)
	jsonOutput := false
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&config.Address, "target", "", "target host:port")
	flags.StringVar(&config.Authority, "authority", "", "HTTP Host authority; defaults to target")
	flags.StringVar(&config.Path, "path", config.Path, "origin-form request path")
	flags.IntVar(&config.Workers, "concurrency", config.Workers, "bounded worker count")
	flags.IntVar(&config.MaxRequests, "requests", config.MaxRequests, "maximum request count")
	flags.IntVar(&config.RatePerSecond, "rate-per-second", 0, "paced requests per second; zero is unpaced")
	flags.IntVar(&durationMS, "duration-ms", durationMS, "maximum duration in milliseconds")
	flags.IntVar(&timeoutMS, "timeout-ms", timeoutMS, "per-request timeout in milliseconds")
	flags.BoolVar(&jsonOutput, "json", false, "write JSON results")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s bench --target HOST:PORT [options]\n", programName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() != 0 || config.Address == "" {
		fmt.Fprintf(stderr, "%s bench: --target is required and positional arguments are not accepted\n", programName)
		return exitUsage
	}
	config.Duration = time.Duration(durationMS) * time.Millisecond
	config.Timeout = time.Duration(timeoutMS) * time.Millisecond
	config.setDefaults()
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "%s bench: invalid configuration: %v\n", programName, err)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := runBenchmark(ctx, config)
	if err != nil {
		fmt.Fprintf(stderr, "%s bench: %v\n", programName, err)
		return exitFailure
	}
	if jsonOutput {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	} else {
		fmt.Fprintf(stdout, "Anvil benchmark: completed=%d offered=%d throughput=%.2f req/s p50=%.3fms p95=%.3fms p99=%.3fms new_connections=%d reused_requests=%d peak_in_flight=%d\n", result.Completed, result.OfferedRequests, result.RequestsPerSec, result.Latency.P50MS, result.Latency.P95MS, result.Latency.P99MS, result.NewConnections, result.ReusedRequests, result.PeakInFlight)
	}
	return exitOK
}
