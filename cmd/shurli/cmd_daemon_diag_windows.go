//go:build windows

package main

// SIGUSR1 does not exist on Windows. The diagnostic snapshot is unavailable;
// return a no-op stop function so the daemon shutdown path stays uniform.
func installDiagSignalHandler(rt *serveRuntime) (stop func()) {
	return func() {}
}
