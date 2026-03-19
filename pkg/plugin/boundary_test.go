package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// B1: Plugin name length boundary.
func TestBoundary_PluginNameLength(t *testing.T) {
	// 64-char accepted.
	name64 := strings.Repeat("a", 64)
	if err := validatePluginName(name64); err != nil {
		t.Errorf("64-char name should be accepted: %v", err)
	}

	// 65-char rejected.
	name65 := strings.Repeat("a", 65)
	if err := validatePluginName(name65); err == nil {
		t.Error("65-char name should be rejected")
	}
}

// B2: Protocol name length boundary.
func TestBoundary_ProtocolNameLength(t *testing.T) {
	name64 := strings.Repeat("a", 64)
	if err := validateProtocolName(name64); err != nil {
		t.Errorf("64-char protocol name should be accepted: %v", err)
	}

	name65 := strings.Repeat("a", 65)
	if err := validateProtocolName(name65); err == nil {
		t.Error("65-char protocol name should be rejected")
	}
}

// B3: Command name length boundary.
func TestBoundary_CommandNameLength(t *testing.T) {
	name64 := strings.Repeat("a", 64)
	if !isValidCommandName(name64) {
		t.Error("64-char command name should be accepted")
	}

	name65 := strings.Repeat("a", 65)
	if isValidCommandName(name65) {
		t.Error("65-char command name should be rejected")
	}
}

// B4: Start timeout boundary.
func TestBoundary_StartTimeout(t *testing.T) {
	orig := startTimeoutDuration
	startTimeoutDuration = 200 * time.Millisecond
	defer func() { startTimeoutDuration = orig }()

	// Start at timeout-50ms -> succeeds.
	r := newTestRegistryB1()
	fast := newSlowPlugin("fast-start", 150*time.Millisecond, "start")
	r.Register(fast)
	if err := r.Enable("fast-start"); err != nil {
		t.Errorf("Start under timeout should succeed: %v", err)
	}

	// Start at timeout+100ms -> times out.
	r2 := newTestRegistryB1()
	slow := newSlowPlugin("slow-start", 300*time.Millisecond, "start")
	r2.Register(slow)
	if err := r2.Enable("slow-start"); err == nil {
		t.Error("Start over timeout should fail")
	}
}

// B5: Drain timeout boundary.
func TestBoundary_DrainTimeout(t *testing.T) {
	origDrain := drainTimeoutDuration
	drainTimeoutDuration = 200 * time.Millisecond
	defer func() { drainTimeoutDuration = origDrain }()

	// Stop under timeout -> succeeds cleanly.
	r := newTestRegistryB1()
	fast := newSlowPlugin("fast-stop", 100*time.Millisecond, "stop")
	r.Register(fast)
	r.Enable("fast-stop")

	start := time.Now()
	r.Disable("fast-stop")
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Errorf("fast stop should complete within drain timeout, took %s", elapsed)
	}

	// Stop over timeout -> forced stopped.
	r2 := newTestRegistryB1()
	slow := newSlowPlugin("slow-stop2", 400*time.Millisecond, "stop")
	r2.Register(slow)
	r2.Enable("slow-stop2")

	start = time.Now()
	r2.Disable("slow-stop2")
	elapsed = time.Since(start)
	// Should timeout at ~200ms, not wait 400ms.
	if elapsed > 350*time.Millisecond {
		t.Errorf("slow stop should be forced at drain timeout, took %s", elapsed)
	}

	info, _ := r2.GetInfo("slow-stop2")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after drain timeout, got %s", info.State)
	}
}

// B6: Circuit breaker threshold boundary.
func TestBoundary_CircuitBreakerThreshold(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("cb-boundary")
	r.Register(p)
	r.Enable("cb-boundary")

	// 2 panics -> still ACTIVE.
	r.recordCrash("cb-boundary")
	r.recordCrash("cb-boundary")
	info, _ := r.GetInfo("cb-boundary")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after 2 crashes, got %s", info.State)
	}

	// 3rd panic -> auto-disabled.
	r.recordCrash("cb-boundary")
	info, _ = r.GetInfo("cb-boundary")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after 3rd crash, got %s", info.State)
	}
}

// B7: Enable/Disable cooldown boundary.
func TestBoundary_EnableDisableCooldown(t *testing.T) {
	enableDisableCooldown = 100 * time.Millisecond
	defer func() { enableDisableCooldown = 0 }()

	r := NewRegistry(&ContextProvider{})
	p := newMinimalPlugin("cooldown")
	r.Register(p)
	r.Enable("cooldown")
	r.Disable("cooldown")

	// Immediately -> rejected.
	err := r.Enable("cooldown")
	if err == nil {
		t.Error("enable within cooldown should be rejected")
	}

	// After cooldown -> allowed.
	time.Sleep(150 * time.Millisecond)
	err = r.Enable("cooldown")
	if err != nil {
		t.Errorf("enable after cooldown should succeed: %v", err)
	}
}

// B8: Max config file size boundary.
func TestBoundary_MaxConfigFileSize(t *testing.T) {
	tmpDir := t.TempDir()

	// 1MB file -> accepted.
	oneMB := make([]byte, 1<<20)
	path := tmpDir + "/ok.yaml"
	if err := writeTestConfig(path, string(oneMB)); err != nil {
		t.Fatal(err)
	}

	data, err := readFileLimited(path, maxConfigFileSize)
	if err != nil {
		t.Errorf("1MB file should be accepted: %v", err)
	}
	if len(data) != 1<<20 {
		t.Errorf("expected %d bytes, got %d", 1<<20, len(data))
	}

	// >1MB file -> rejected.
	overMB := make([]byte, (1<<20)+1)
	pathOver := tmpDir + "/big.yaml"
	if err := writeTestConfig(pathOver, string(overMB)); err != nil {
		t.Fatal(err)
	}

	_, err = readFileLimited(pathOver, maxConfigFileSize)
	if err == nil {
		t.Error(">1MB file should be rejected")
	}
}

// B9: Zero plugins -> AllCommands/AllRoutes/DisableAll work.
func TestBoundary_ZeroPlugins(t *testing.T) {
	r := newTestRegistryB1()

	cmds := r.AllCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands, got %d", len(cmds))
	}

	routes := r.AllRoutes()
	if len(routes) != 0 {
		t.Errorf("expected 0 routes, got %d", len(routes))
	}

	count, err := r.DisableAll()
	if err != nil {
		t.Errorf("DisableAll with 0 plugins should not error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 disabled, got %d", count)
	}
}

// B10: Plugin name edge cases.
func TestBoundary_PluginNameEdgeCases(t *testing.T) {
	accepted := []string{"a", "ab", "a-b", "a1", "a1b", "abc"}
	for _, name := range accepted {
		if err := validatePluginName(name); err != nil {
			t.Errorf("name %q should be accepted: %v", name, err)
		}
	}

	rejected := []string{
		"-a",   // starts with hyphen
		"a-",   // ends with hyphen
		"A",    // uppercase
		"a b",  // space
		"",     // empty
		"a.b",  // dot
		"a_b",  // underscore
	}
	for _, name := range rejected {
		if err := validatePluginName(name); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

// Silence unused import warning.
var _ = context.Background
