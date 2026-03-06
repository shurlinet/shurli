// Package termcolor provides simple ANSI terminal color output.
//
// This replaces the github.com/fatih/color dependency with minimal ANSI
// escape codes. Only the functions actually used by Shurli are provided.
//
// Inspired by the API of github.com/fatih/color (MIT License).
// Copyright (c) 2013 Fatih Arslan. See THIRD_PARTY_NOTICES in the repo root.
package termcolor

import (
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
	faint  = "\033[2m"
)

var (
	ttyOnce   sync.Once
	ttyResult bool
)

// isColorEnabled reports whether color output should be used.
// Disabled when stdout is not a terminal or NO_COLOR env is set.
func isColorEnabled() bool {
	ttyOnce.Do(func() {
		if os.Getenv("NO_COLOR") != "" {
			return
		}
		fi, err := os.Stdout.Stat()
		if err != nil {
			return
		}
		ttyResult = fi.Mode()&os.ModeCharDevice != 0
	})
	return ttyResult
}

// Green prints a green-colored line to stdout (appends newline).
func Green(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Printf("%s%s%s\n", green, msg, reset)
	} else {
		fmt.Println(msg)
	}
}

// Red prints a red-colored line to stdout (appends newline).
func Red(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Printf("%s%s%s\n", red, msg, reset)
	} else {
		fmt.Println(msg)
	}
}

// Yellow prints a yellow-colored line to stdout (appends newline).
func Yellow(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Printf("%s%s%s\n", yellow, msg, reset)
	} else {
		fmt.Println(msg)
	}
}

// Faint prints faint/dim text to stdout (no newline appended - Printf style).
func Faint(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Print(faint + msg + reset)
	} else {
		fmt.Print(msg)
	}
}

// Writer-accepting variants for use with io.Writer (e.g., in doStatus).
// These write colored text without appending a newline (Printf style).

// Wgreen writes green text to w. No newline appended.
func Wgreen(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, green+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}

// Wred writes red text to w. No newline appended.
func Wred(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, red+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}

// Wyellow writes yellow text to w. No newline appended.
func Wyellow(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, yellow+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}

// Wblue writes blue text to w. No newline appended.
func Wblue(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, blue+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}

// Wcyan writes cyan text to w. No newline appended.
func Wcyan(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, cyan+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}

// Wfaint writes faint/dim text to w. No newline appended.
func Wfaint(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isColorEnabled() {
		fmt.Fprint(w, faint+msg+reset)
	} else {
		fmt.Fprint(w, msg)
	}
}
