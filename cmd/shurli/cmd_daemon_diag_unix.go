//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"
)

// diagSnapshotMinInterval is the minimum gap between two SIGUSR1-triggered
// snapshots. Prevents a signal storm from filling $TMPDIR or starving the
// daemon while it produces back-to-back snapshots. 1s is fast enough for
// manual kick-testing and slow enough to bound disk usage.
const diagSnapshotMinInterval = 1 * time.Second

// diagStopTimeout bounds how long installDiagSignalHandler's returned
// stop() blocks while the snapshot goroutine finishes any in-flight write.
// Keeps daemon shutdown deterministic even if the filesystem hangs.
const diagStopTimeout = 5 * time.Second

// installDiagSignalHandler wires SIGUSR1 → diagSnapshot. The returned
// stop function unregisters the signal handler and drains the goroutine;
// daemon shutdown calls it so the handler does not outlive the host.
//
// Trigger: kill -USR1 $(pgrep -f "shurli daemon start")
// Output:  $TMPDIR/shurli-diag-<timestamp>-<pid>.txt (mode 0600)
func installDiagSignalHandler(rt *serveRuntime) (stop func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var lastSnapshot time.Time
	var mu sync.Mutex

	go func() {
		// A diagnostic tool must never crash the process it is observing.
		// Recover any panic from diagSnapshot (e.g. a future libp2p type
		// change breaking a type assertion) and log it with a stack trace.
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("diag: snapshot goroutine panic recovered",
					"panic", r, "stack", string(debug.Stack()))
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					// Channel closed by stop(). Exit without writing a
					// phantom snapshot from the zero-value signal.
					return
				}
				mu.Lock()
				if since := time.Since(lastSnapshot); since < diagSnapshotMinInterval {
					mu.Unlock()
					slog.Warn("diag: snapshot rate-limited",
						"min_interval", diagSnapshotMinInterval, "since_last", since)
					continue
				}
				lastSnapshot = time.Now()
				mu.Unlock()
				runDiagSnapshotOnce(rt)
			}
		}
	}()

	return func() {
		// Order matters:
		//   1. signal.Stop unregisters the channel from the runtime so no
		//      further SIGUSR1 will be delivered to it.
		//   2. cancel() unblocks the goroutine via ctx.Done if it is
		//      currently waiting on the select.
		//   3. close(ch) wakes any select already dequeuing from ch.
		//   4. wait up to diagStopTimeout for any in-flight snapshot to
		//      finish; log and return if it doesn't (daemon shutdown
		//      watchdog will force exit at the 60s outer limit).
		signal.Stop(ch)
		cancel()
		close(ch)
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(diagStopTimeout):
			slog.Warn("diag: stop timeout waiting for snapshot goroutine",
				"timeout", diagStopTimeout)
		}
	}
}

// runDiagSnapshotOnce is the single-shot snapshot path. Wraps panics so the
// signal goroutine survives even if a future diagSnapshot bug would have
// crashed it, and buffers the output so a partial write surfaces as a real
// error instead of a silently truncated file.
func runDiagSnapshotOnce(rt *serveRuntime) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("diag: diagSnapshot panic recovered",
				"panic", r, "stack", string(debug.Stack()))
		}
	}()

	ts := time.Now().Format("20060102-150405.000000000")
	name := fmt.Sprintf("shurli-diag-%s-%d.txt", ts, os.Getpid())
	path := filepath.Join(os.TempDir(), name)
	// O_EXCL prevents clobbering an existing file if two sub-nanosecond
	// snapshots collide; 0600 keeps peer IDs and interface addresses out
	// of other local users' reach.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		// Log only the basename. The full path may contain a per-user
		// $TMPDIR component (macOS /var/folders/<hash>/T/<user>/...).
		slog.Error("diag: create snapshot file failed", "file", name, "err", err)
		return
	}
	// Single point of truth for the final close-err / flush-err handling.
	// Do NOT double-close f: bufio does not own f, only the caller does.
	bw := bufio.NewWriterSize(f, 64<<10)
	diagSnapshot(rt.network.Host(), bw)
	flushErr := bw.Flush()
	closeErr := f.Close()
	if flushErr != nil {
		slog.Error("diag: flush snapshot buffer failed", "file", name, "err", flushErr)
		return
	}
	if closeErr != nil {
		slog.Error("diag: close snapshot file failed", "file", name, "err", closeErr)
		return
	}
	slog.Info("diag: snapshot written", "file", name, "dir", os.TempDir())
}
