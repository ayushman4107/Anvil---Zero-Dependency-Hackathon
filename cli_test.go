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
	if !strings.Contains(stdout.String(), phase0Status) {
		t.Fatalf("proxy help does not identify Phase 0 status: %q", stdout.String())
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
