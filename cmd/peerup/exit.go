package main

import (
	"fmt"
	"os"
)

// osExit wraps os.Exit so tests can intercept process termination.
// Tests replace this with a function that panics with exitSentinel,
// allowing panic/recover to capture the exit code and stop execution
// at the exact call site  - just like a real os.Exit would.
var osExit = os.Exit

// exitSentinel is the panic value used by test overrides of osExit.
// The int value is the exit code.
type exitSentinel int

// fatal prints a formatted error message to stderr and exits with code 1.
// It replaces log.Fatalf throughout cmd/peerup so that tests can intercept
// the exit via the injectable osExit variable.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	osExit(1)
}
