package watchdog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunHealthy(t *testing.T) {
	// Suppress slog output during tests
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var checkCount atomic.Int32
	checks := []HealthCheck{
		{
			Name: "test",
			Check: func() error {
				checkCount.Add(1)
				return nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, Config{Interval: 50 * time.Millisecond}, checks)
		close(done)
	}()

	// Wait for at least 2 checks
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	count := checkCount.Load()
	if count < 2 {
		t.Errorf("expected at least 2 health checks, got %d", count)
	}
}

func TestRunUnhealthy(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var healthyCount, unhealthyCount atomic.Int32
	checks := []HealthCheck{
		{
			Name: "healthy",
			Check: func() error {
				healthyCount.Add(1)
				return nil
			},
		},
		{
			Name: "broken",
			Check: func() error {
				unhealthyCount.Add(1)
				return errors.New("something wrong")
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, Config{Interval: 50 * time.Millisecond}, checks)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if healthyCount.Load() < 2 {
		t.Errorf("healthy check ran %d times, want >= 2", healthyCount.Load())
	}
	if unhealthyCount.Load() < 2 {
		t.Errorf("unhealthy check ran %d times, want >= 2", unhealthyCount.Load())
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	done := make(chan struct{})
	go func() {
		Run(ctx, Config{Interval: time.Hour}, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return on cancelled context")
	}
}

func TestRunDefaultInterval(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately - we just want to verify it doesn't panic with zero config
	cancel()

	Run(ctx, Config{}, nil) // zero interval = should use default 30s
}

func TestSdNotifyNoSocket(t *testing.T) {
	// Ensure NOTIFY_SOCKET is not set
	os.Unsetenv("NOTIFY_SOCKET")

	// All should be no-ops (return nil)
	if err := Ready(); err != nil {
		t.Errorf("Ready() = %v, want nil", err)
	}
	if err := Watchdog(); err != nil {
		t.Errorf("Watchdog() = %v, want nil", err)
	}
	if err := Stopping(); err != nil {
		t.Errorf("Stopping() = %v, want nil", err)
	}
}

func TestSdNotifyBadSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/nonexistent/socket.sock")

	err := Ready()
	if err == nil {
		t.Error("Ready() with bad socket should return error")
	}
}
