package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("run help exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "anvil proxy") && !strings.Contains(stdout.String(), "proxy") {
		t.Fatalf("help output does not describe proxy command: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Phase 1 raw-TCP lifecycle proof") {
		t.Fatalf("help output has stale lifecycle status: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Phase 2 strict HTTP/1.1") {
		t.Fatalf("help output has stale codec status: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Phase 3 network-facing server/router") {
		t.Fatalf("help output has stale server/router status: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("help wrote to stderr: %q", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"unknown"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("unknown command exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr does not explain failure: %q", stderr.String())
	}
}

func TestPlannedCommandHelpIsHonest(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"proxy", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("proxy help exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), plannedStatus) {
		t.Fatalf("proxy help does not identify planned status: %q", stdout.String())
	}
}

func TestDevEchoHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"dev-echo", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("dev-echo help exit code = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: anvil dev-echo") {
		t.Fatalf("dev-echo help missing usage: %q", stderr.String())
	}
}

func TestDevHTTPHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"dev-http", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("dev-http help exit code = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: anvil dev-http") {
		t.Fatalf("dev-http help missing usage: %q", stderr.String())
	}
}
