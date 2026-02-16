package main

// runServe is an alias for runDaemon — the daemon command replaces serve.
// Kept for backward compatibility so existing docs and scripts continue to work.
func runServe(args []string) {
	runDaemon(args)
}
