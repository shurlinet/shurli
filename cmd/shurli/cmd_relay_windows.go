//go:build windows

package main

// escalateToFileOwner is a no-op on Windows (no Unix-style file ownership).
func escalateToFileOwner(_ string, _ []string) {}
