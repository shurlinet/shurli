package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"
)

// Config holds watchdog configuration.
type Config struct {
	Interval time.Duration // health check interval (default: 30s)
}

// HealthCheck is a named function that returns nil if healthy.
type HealthCheck struct {
	Name  string
	Check func() error
}

// Run starts the watchdog loop. It runs health checks at the configured interval,
// logs failures via slog, and sends WATCHDOG=1 to systemd on success.
// Blocks until ctx is cancelled.
func Run(ctx context.Context, cfg Config, checks []HealthCheck) {
	interval := cfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, hc := range checks {
				if err := hc.Check(); err != nil {
					slog.Warn("health check failed", "check", hc.Name, "error", err)
				}
			}
			// Always heartbeat. The watchdog proves "I'm alive",
			// not "all checks pass". Health issues are logged above.
			Watchdog()
		}
	}
}

// --- systemd sd_notify (pure Go, no CGo) ---

// Ready sends READY=1 to systemd, indicating the service is started.
// No-op if NOTIFY_SOCKET is not set (non-systemd environments like macOS).
func Ready() error {
	return sdNotify("READY=1")
}

// Watchdog sends WATCHDOG=1 to systemd, resetting the watchdog timer.
// No-op if NOTIFY_SOCKET is not set.
func Watchdog() error {
	return sdNotify("WATCHDOG=1")
}

// Stopping sends STOPPING=1 to systemd, indicating graceful shutdown.
// No-op if NOTIFY_SOCKET is not set.
func Stopping() error {
	return sdNotify("STOPPING=1")
}

// sdNotify sends a message to the systemd notify socket.
// Returns nil if NOTIFY_SOCKET is not set (non-systemd environment).
func sdNotify(state string) error {
	socketPath := os.Getenv("NOTIFY_SOCKET")
	if socketPath == "" {
		return nil
	}

	// systemd supports abstract sockets (prefixed with @) and filesystem sockets
	socketAddr := &net.UnixAddr{
		Name: socketPath,
		Net:  "unixgram",
	}

	conn, err := net.DialUnix("unixgram", nil, socketAddr)
	if err != nil {
		return fmt.Errorf("sd_notify: dial: %w", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte(state))
	if err != nil {
		return fmt.Errorf("sd_notify: write: %w", err)
	}
	return nil
}
