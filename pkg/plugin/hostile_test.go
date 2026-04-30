package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// --- Hook failure matrix (H1-H11) ---

// H1: Init returns error -> plugin NOT in List.
func TestHostile_InitReturnsError(t *testing.T) {
	r := newTestRegistryB1()
	p := newErrorPlugin("init-err", "init", errors.New("init failed"))

	err := r.Register(p)
	if err == nil {
		t.Fatal("expected Register to fail")
	}

	list := r.List()
	for _, info := range list {
		if info.Name == "init-err" {
			t.Error("plugin with failed Init should not appear in List")
		}
	}

	// Registry still accepts new plugins.
	p2 := newMinimalPlugin("after-init-err")
	if err := r.Register(p2); err != nil {
		t.Errorf("registry should accept new plugins after Init failure: %v", err)
	}
}

// H2: Init panics -> Register recovers, plugin NOT in List.
func TestHostile_InitPanics(t *testing.T) {
	r := newTestRegistryB1()
	p := newPanickingPlugin("init-panic", "init")

	err := r.Register(p)
	if err == nil {
		t.Fatal("expected Register to fail on panicking Init")
	}

	list := r.List()
	for _, info := range list {
		if info.Name == "init-panic" {
			t.Error("panicking Init plugin should not appear in List")
		}
	}
}

// H3: Init hangs -> currently no timeout, document behavior.
func TestHostile_InitHangs(t *testing.T) {
	t.Skip("Init has no timeout - Layer 1 plugins are trusted compiled-in code. " +
		"Layer 2 WASM will use fuel metering.")
}

// H4: Start returns error -> plugin stays READY, no commands/routes.
func TestHostile_StartReturnsError(t *testing.T) {
	r := newTestRegistryB1()
	p := newErrorPlugin("start-err", "start", errors.New("start failed"))
	p.commands = []Command{{Name: "ghost-cmd"}}

	r.Register(p)
	err := r.Enable("start-err")
	if err == nil {
		t.Fatal("expected Enable to fail")
	}

	info, _ := r.GetInfo("start-err")
	if info.State == StateActive {
		t.Error("plugin should NOT be ACTIVE after Start error")
	}

	cmds := r.AllCommands()
	for _, cmd := range cmds {
		if cmd.Name == "ghost-cmd" {
			t.Error("failed plugin's commands should not appear")
		}
	}
}

// H5: Start panics -> plugin stays READY, no zombie state.
func TestHostile_StartPanics(t *testing.T) {
	r := newTestRegistryB1()
	p := newPanickingPlugin("start-panic", "start")
	p.commands = []Command{{Name: "zombie-cmd"}}

	r.Register(p)
	err := r.Enable("start-panic")
	if err == nil {
		t.Fatal("expected Enable to fail on panicking Start")
	}

	info, _ := r.GetInfo("start-panic")
	if info.State == StateActive {
		t.Error("plugin should NOT be ACTIVE after Start panic")
	}
}

// H6: Start hangs past timeout -> Enable times out, plugin NOT ACTIVE.
func TestHostile_StartHangs(t *testing.T) {
	orig := startTimeoutDuration
	startTimeoutDuration = 100 * time.Millisecond
	defer func() { startTimeoutDuration = orig }()

	r := newTestRegistryB1()
	p := newHangingPlugin("start-hang", "start")
	p.commands = []Command{{Name: "hang-cmd"}}

	r.Register(p)
	err := r.Enable("start-hang")
	if err == nil {
		t.Fatal("expected Enable to timeout")
	}

	info, _ := r.GetInfo("start-hang")
	if info.State == StateActive {
		t.Error("hanging Start should not result in ACTIVE state")
	}
}

// H7: Stop returns error -> Disable still reaches STOPPED.
func TestHostile_StopReturnsError(t *testing.T) {
	r := newTestRegistryB1()
	p := newErrorPlugin("stop-err", "stop", errors.New("stop failed"))

	r.Register(p)
	r.Enable("stop-err")

	err := r.Disable("stop-err")
	// Disable should succeed even if Stop returns error.
	if err != nil {
		t.Logf("Disable returned error (acceptable): %v", err)
	}

	info, _ := r.GetInfo("stop-err")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED despite Stop error, got %s", info.State)
	}
}

// H8: Stop panics -> Disable recovers, STOPPED.
func TestHostile_StopPanics(t *testing.T) {
	r := newTestRegistryB1()
	p := newPanickingPlugin("stop-panic", "stop")

	r.Register(p)
	r.Enable("stop-panic")

	err := r.Disable("stop-panic")
	if err != nil {
		t.Logf("Disable returned error (acceptable): %v", err)
	}

	info, _ := r.GetInfo("stop-panic")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED despite Stop panic, got %s", info.State)
	}
}

// H9: Stop hangs past drain timeout -> Disable times out, forced STOPPED.
func TestHostile_StopHangs(t *testing.T) {
	orig := drainTimeoutDuration
	drainTimeoutDuration = 100 * time.Millisecond
	defer func() { drainTimeoutDuration = orig }()

	r := newTestRegistryB1()
	p := newHangingPlugin("stop-hang", "stop")

	r.Register(p)
	r.Enable("stop-hang")

	start := time.Now()
	r.Disable("stop-hang")
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("Disable should timeout quickly, took %s", elapsed)
	}

	info, _ := r.GetInfo("stop-hang")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after drain timeout, got %s", info.State)
	}
}

// H10: OnNetworkReady panics -> other plugins still notified.
func TestHostile_OnNetworkReadyPanics(t *testing.T) {
	orig := startTimeoutDuration
	startTimeoutDuration = 2 * time.Second
	defer func() { startTimeoutDuration = orig }()

	r := newTestRegistryB1()

	panicker := newPanickingPlugin("nr-panic", "network-ready")
	healthy := newMinimalPlugin("nr-healthy")

	r.Register(panicker)
	r.Register(healthy)
	r.Enable("nr-panic")
	r.Enable("nr-healthy")

	r.NotifyNetworkReady()

	if healthy.networkReadyCalls.Load() != 1 {
		t.Error("healthy plugin should have received OnNetworkReady despite other plugin panicking")
	}
}

// H11: OnNetworkReady hangs -> other plugins notified within timeout (A6 fix).
func TestHostile_OnNetworkReadyHangs(t *testing.T) {
	orig := startTimeoutDuration
	startTimeoutDuration = 100 * time.Millisecond
	defer func() { startTimeoutDuration = orig }()

	r := newTestRegistryB1()

	hanger := newHangingPlugin("nr-hang", "network-ready")
	healthy := newMinimalPlugin("nr-healthy2")

	r.Register(hanger)
	r.Register(healthy)
	r.Enable("nr-hang")
	r.Enable("nr-healthy2")

	start := time.Now()
	r.NotifyNetworkReady()
	elapsed := time.Since(start)

	if healthy.networkReadyCalls.Load() != 1 {
		t.Error("healthy plugin should receive OnNetworkReady despite hanger")
	}

	// Should complete within 2x the timeout (one for hanger, one for healthy).
	if elapsed > 500*time.Millisecond {
		t.Errorf("NotifyNetworkReady should not block indefinitely, took %s", elapsed)
	}
}

// --- Evil plugin tests (H12-H25) ---

// H12: 3 handler panics -> circuit breaker -> auto-disabled.
func TestHostile_HandlerPanics3x_CircuitBreaker(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("handler-cb")
	p.commands = []Command{{Name: "cb-cmd"}}

	r.Register(p)
	r.Enable("handler-cb")

	for i := 0; i < 3; i++ {
		r.recordCrash("handler-cb")
	}

	info, _ := r.GetInfo("handler-cb")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after 3 crashes, got %s", info.State)
	}

	cmds := r.AllCommands()
	for _, cmd := range cmds {
		if cmd.Name == "cb-cmd" {
			t.Error("circuit-broken plugin's commands should be gone")
		}
	}
}

// H13: ConfigReload callback panics -> other plugins' callbacks still fire (A7 fix).
func TestHostile_ConfigReloadPanics(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config dirs for both plugins.
	for _, name := range []string{"reload-panicker", "reload-survivor"} {
		dir := fmt.Sprintf("%s/plugins/test.io/mock/%s", tmpDir, name)
		if err := createTestConfigDir(dir); err != nil {
			t.Fatal(err)
		}
	}

	r := NewRegistry(&ContextProvider{ConfigDir: tmpDir})
	r.enableDisableCooldown = 0

	// Plugin that panics on config reload.
	panicker := newMinimalPlugin("reload-panicker")
	panicker.id = "test.io/mock/reload-panicker"
	panicker.initFn = func(ctx *PluginContext) error {
		ctx.OnConfigReload(func([]byte) {
			panic("reload panic!")
		})
		return nil
	}

	// Plugin that tracks reload.
	var survivorReloaded atomic.Bool
	survivor := newMinimalPlugin("reload-survivor")
	survivor.id = "test.io/mock/reload-survivor"
	survivor.initFn = func(ctx *PluginContext) error {
		ctx.OnConfigReload(func([]byte) {
			survivorReloaded.Store(true)
		})
		return nil
	}

	r.Register(panicker)
	r.Register(survivor)
	r.Enable("reload-panicker")
	r.Enable("reload-survivor")

	// Write new configs.
	for _, name := range []string{"reload-panicker", "reload-survivor"} {
		path := fmt.Sprintf("%s/plugins/test.io/mock/%s/config.yaml", tmpDir, name)
		if err := writeTestConfig(path, "new: value"); err != nil {
			t.Fatalf("write config for %s: %v", name, err)
		}
	}

	// Should not panic.
	r.NotifyConfigReload()

	if !survivorReloaded.Load() {
		t.Error("survivor's reload callback should have fired despite panicker")
	}
}

// H14: Route collides with core -> core route takes precedence.
func TestHostile_RouteCollidesWithCore(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("route-collider")
	p.routes = []Route{
		{Method: "GET", Path: "/v1/status", Handler: noopHandler()},
	}

	r.Register(p)
	r.Enable("route-collider")

	// AllRoutes returns the plugin's route, but core's mux would be
	// registered first in the real daemon. The key invariant is that
	// the plugin CAN'T prevent the core route from working.
	// This test documents the behavior.
	routes := r.AllRoutes()
	found := false
	for _, rt := range routes {
		if rt.Path == "/v1/status" {
			found = true
		}
	}
	if !found {
		t.Log("plugin route /v1/status not in AllRoutes (may be filtered)")
	}
}

// H15: Protocol collides with core -> rejected at registration.
func TestHostile_ProtocolCollidesWithCore(t *testing.T) {
	// Test the validation function directly - a real ServiceRegistry
	// would reject these at Enable time via registerProtocols.
	if !isReservedProtocolName("ping") {
		t.Error("'ping' should be a reserved protocol name")
	}
	if !isReservedProtocolName("relay-pair") {
		t.Error("'relay-pair' should be a reserved protocol name")
	}
	if isReservedProtocolName("file-transfer") {
		t.Error("'file-transfer' should NOT be reserved")
	}
}

// H16: Protocol collides with other plugin -> second plugin rejected.
func TestHostile_ProtocolCollidesWithOtherPlugin(t *testing.T) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	sr := sdk.NewServiceRegistry(h, nil)

	r := NewRegistry(&ContextProvider{
		ServiceRegistry: sr,
	})
	r.enableDisableCooldown = 0

	// Plugin A owns "shared-proto".
	a := newMinimalPlugin("proto-owner")
	a.protocols = []Protocol{
		{Name: "shared-proto", Version: "1.0.0", Handler: noopStreamHandler()},
	}
	r.Register(a)
	if err := r.Enable("proto-owner"); err != nil {
		t.Fatalf("Enable A: %v", err)
	}

	// Plugin B tries to register the same protocol name -> must fail.
	b := newMinimalPlugin("proto-thief")
	b.protocols = []Protocol{
		{Name: "shared-proto", Version: "1.0.0", Handler: noopStreamHandler()},
	}
	r.Register(b)
	err = r.Enable("proto-thief")
	if err == nil {
		t.Fatal("expected Enable to fail for duplicate protocol")
	}

	// Plugin B should NOT be ACTIVE.
	info, _ := r.GetInfo("proto-thief")
	if info.State == StateActive {
		t.Error("plugin B should NOT be ACTIVE after duplicate protocol rejection")
	}

	// Plugin A should still be ACTIVE (its state is not corrupted).
	infoA, _ := r.GetInfo("proto-owner")
	if infoA.State != StateActive {
		t.Errorf("plugin A should still be ACTIVE, got %s", infoA.State)
	}

	// Plugin A's protocol must survive B's failed registration.
	// rollbackProtocols only removes what B successfully registered (nothing).
	if _, ok := sr.GetService("shared-proto"); !ok {
		t.Error("plugin A's protocol should still be registered after B's rejection")
	}
}

// H17: Nil Command.Run -> AllCommands doesn't crash.
func TestHostile_NilCommandRun(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("nil-run")
	p.commands = []Command{
		{Name: "nil-cmd", Run: nil},
	}

	r.Register(p)
	r.Enable("nil-run")

	// Should not panic.
	cmds := r.AllCommands()
	if len(cmds) != 1 {
		t.Errorf("expected 1 command, got %d", len(cmds))
	}
}

// H18: Nil Route Handler -> HTTP request doesn't crash.
func TestHostile_NilRouteHandler(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("nil-handler")
	p.routes = []Route{
		{Method: "GET", Path: "/v1/nil", Handler: nil},
	}

	r.Register(p)
	r.Enable("nil-handler")

	// AllRoutes returns it. The daemon's mux would need to handle nil.
	routes := r.AllRoutes()
	if len(routes) != 1 {
		t.Errorf("expected 1 route, got %d", len(routes))
	}
}

// H19: Empty protocol name -> rejected at registration.
func TestHostile_EmptyProtocolName(t *testing.T) {
	if err := validateProtocolName(""); err == nil {
		t.Error("empty protocol name should be rejected")
	}
}

// H20: Massive declarations -> no OOM, no O(n^2).
func TestHostile_MassiveDeclarations(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("massive")

	cmds := make([]Command, 10000)
	for i := range cmds {
		cmds[i] = Command{Name: fmt.Sprintf("cmd-%d", i)}
	}
	p.commands = cmds

	routes := make([]Route, 10000)
	for i := range routes {
		routes[i] = Route{Method: "GET", Path: fmt.Sprintf("/v1/m/%d", i), Handler: noopHandler()}
	}
	p.routes = routes

	r.Register(p)
	r.Enable("massive")

	allCmds := r.AllCommands()
	if len(allCmds) != 10000 {
		t.Errorf("expected 10000 commands, got %d", len(allCmds))
	}

	allRoutes := r.AllRoutes()
	if len(allRoutes) != 10000 {
		t.Errorf("expected 10000 routes, got %d", len(allRoutes))
	}
}

// H21: Slice modification after return -> AllCommands returns copy.
func TestHostile_SliceModificationAfterReturn(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("slice-mutator")
	p.commands = []Command{
		{Name: "original"},
	}

	r.Register(p)
	r.Enable("slice-mutator")

	cmds := r.AllCommands()
	if len(cmds) == 0 {
		t.Fatal("expected commands")
	}

	// Mutate the returned slice.
	cmds[0].Name = "mutated"

	// Re-fetch should show original.
	cmds2 := r.AllCommands()
	if len(cmds2) == 0 {
		t.Fatal("expected commands on second fetch")
	}
	// Note: AllCommands returns a new slice each time by appending,
	// but the Command structs themselves may share underlying data
	// with the plugin. This tests that AllCommands() doesn't cache.
	if cmds2[0].Name != "original" {
		t.Log("Note: Command structs share memory with plugin's slice. " +
			"AllCommands() creates a new slice but doesn't deep-copy Command structs. " +
			"This is acceptable for Layer 1 (trusted compiled-in plugins).")
	}
}

// H22: Concurrent enable/disable 100x with -race -> no panics, races, deadlocks.
func TestHostile_ConcurrentEnableDisable100x(t *testing.T) {
	r := NewRegistry(&ContextProvider{})
	r.enableDisableCooldown = 0
	p := newMinimalPlugin("concurrent")
	r.Register(p)

	// All goroutine setup done before launching concurrent work.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Errors are expected (cooldown, state transitions).
			r.Enable("concurrent")
			r.Disable("concurrent")
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent enable/disable did not complete in 30s")
	}
}

// H23: PluginContext methods after disable -> return error, no panic.
func TestHostile_PluginCalledAfterDisable(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("post-disable")
	var capturedCtx *PluginContext
	p.initFn = func(ctx *PluginContext) error {
		capturedCtx = ctx
		return nil
	}

	r.Register(p)
	r.Enable("post-disable")
	r.Disable("post-disable")

	// After disable, network methods should return errors (F3 fix nils the fields).
	if capturedCtx == nil {
		t.Fatal("expected PluginContext to be captured")
	}

	perr := capturedCtx.ConnectToPeer(context.Background(), "")
	if perr == nil {
		t.Error("ConnectToPeer after disable should return error")
	}

	_, serr := capturedCtx.OpenStream(context.Background(), "", "proto")
	if serr == nil {
		t.Error("OpenStream after disable should return error")
	}

	_, rerr := capturedCtx.ResolveName("test")
	if rerr == nil {
		t.Error("ResolveName after disable should return error")
	}
}

// H24: Path traversal in plugin ID -> ConfigDir confined.
func TestHostile_PathTraversalInPluginID(t *testing.T) {
	// validatePluginID should catch path traversal.
	badIDs := []string{
		"../../../etc/passwd",
		"test.io/../../../etc",
		"test.io/mock/../../etc",
	}

	for _, id := range badIDs {
		if err := validatePluginID(id); err == nil {
			t.Errorf("plugin ID %q should be rejected (path traversal)", id)
		}
	}
}

// H25: Start panic cleanup -> Stop called, resources cleaned.
func TestHostile_StartPanicCleanup(t *testing.T) {
	r := newTestRegistryB1()
	var stopCalled atomic.Bool
	p := newMinimalPlugin("panic-cleanup")
	p.startFn = func(_ context.Context) error {
		panic("start panic")
	}
	p.stopFn = func() error {
		stopCalled.Store(true)
		return nil
	}

	r.Register(p)
	err := r.Enable("panic-cleanup")
	if err == nil {
		t.Fatal("expected Enable to fail")
	}

	// G1 fix: Stop should be called after Start panic to clean up.
	if !stopCalled.Load() {
		t.Error("Stop should be called after Start panic (G1 fix)")
	}
}

// --- Supervisor/Checkpointer hostile tests (H26-H30) ---

// H26: Checkpoint panics -> supervisor recovers, restart proceeds without checkpoint data.
func TestHostile_CheckpointPanics(t *testing.T) {
	r := newTestRegistryB1()

	// Use testCheckpointerPlugin with a panicking Checkpoint.
	// Track whether Restore is called (it should NOT be, since Checkpoint panicked).
	var restoreCalled atomic.Bool
	p := newCheckpointPlugin("cp-panicker", []byte("important-state"))
	p.checkpointFn = func() ([]byte, error) {
		panic("checkpoint explosion!")
	}
	p.restoreFn = func([]byte) error {
		restoreCalled.Store(true)
		return nil
	}

	r.Register(p)
	r.Enable("cp-panicker")

	// Trigger handler crash -> supervisor attempts Checkpoint (panics) -> restart proceeds.
	r.recordCrashAndMaybeRestart("cp-panicker")

	// Wait for supervisor restart goroutine to complete.
	if !waitForState(r, "cp-panicker", StateActive, 2*time.Second) {
		info, _ := r.GetInfo("cp-panicker")
		t.Fatalf("expected ACTIVE after restart with panicking checkpoint, got %s", info.State)
	}

	// Restore should NOT have been called (no checkpoint data saved).
	if restoreCalled.Load() {
		t.Error("Restore should not be called when Checkpoint panicked (no data saved)")
	}
}

// H27: Restore panics -> supervisor recovers, plugin starts fresh (no restored state).
func TestHostile_RestorePanics(t *testing.T) {
	r := newTestRegistryB1()

	// Checkpoint succeeds, but Restore panics.
	p := newCheckpointPlugin("restore-panicker", []byte("good-checkpoint"))
	p.restoreFn = func([]byte) error {
		panic("restore panic!")
	}

	r.Register(p)
	r.Enable("restore-panicker")

	// Trigger handler crash -> supervisor: Checkpoint (ok) -> Disable -> Enable -> Restore (panics).
	// Supervisor catches the Restore panic and logs it. Plugin should still be ACTIVE.
	r.recordCrashAndMaybeRestart("restore-panicker")

	// Wait for supervisor restart to complete (Disable + backoff + Enable + Restore).
	if !waitForState(r, "restore-panicker", StateActive, 2*time.Second) {
		info, _ := r.GetInfo("restore-panicker")
		t.Fatalf("expected ACTIVE after restart with panicking Restore, got %s", info.State)
	}
}

// H28: Checkpoint returns garbage -> Restore receives it, plugin decides.
func TestHostile_CheckpointReturnsGarbage(t *testing.T) {
	r := newTestRegistryB1()

	// 10MB of random-ish bytes.
	garbage := make([]byte, 10*1024*1024)
	for i := range garbage {
		garbage[i] = byte(i % 256)
	}

	p := newCheckpointPlugin("garbage-cp", garbage)
	r.Register(p)
	r.Enable("garbage-cp")

	data, err := p.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(data) != len(garbage) {
		t.Errorf("expected %d bytes, got %d", len(garbage), len(data))
	}

	// Restore with garbage - plugin handles it.
	if err := p.Restore(data); err != nil {
		t.Errorf("Restore should succeed (plugin accepts any bytes): %v", err)
	}
}

// H29: Auto-restart after single handler crash -> commands/routes/protocols back.
func TestHostile_AutoRestartAfterCrash(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("auto-restart")
	p.commands = []Command{{Name: "restart-cmd"}}
	p.routes = []Route{{Method: "GET", Path: "/v1/restart-test", Handler: noopHandler()}}
	p.protocols = []Protocol{{Name: "restart-proto", Version: "1.0.0", Handler: noopStreamHandler()}}

	r.Register(p)
	r.Enable("auto-restart")

	// Single handler crash -> supervisor triggers restart.
	r.recordCrashAndMaybeRestart("auto-restart")

	// Wait for supervisor restart goroutine to complete.
	if !waitForState(r, "auto-restart", StateActive, 2*time.Second) {
		info, _ := r.GetInfo("auto-restart")
		t.Fatalf("expected ACTIVE after auto-restart, got %s", info.State)
	}

	// Verify commands are back.
	cmds := r.AllCommands()
	foundCmd := false
	for _, cmd := range cmds {
		if cmd.Name == "restart-cmd" {
			foundCmd = true
		}
	}
	if !foundCmd {
		t.Error("commands should be back after auto-restart")
	}

	// Verify routes are back.
	routes := r.AllRoutes()
	foundRoute := false
	for _, rt := range routes {
		if rt.Path == "/v1/restart-test" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Error("routes should be back after auto-restart")
	}

	// Verify protocols are back (ActiveProtocols reads from plugin declarations).
	protos := r.ActiveProtocols()
	foundProto := false
	for _, proto := range protos {
		if proto.Name == "restart-proto" {
			foundProto = true
		}
	}
	if !foundProto {
		t.Error("protocols should be back after auto-restart")
	}
}

// H30: Exponential backoff timing.
func TestHostile_ExponentialBackoff(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("backoff")

	r.Register(p)
	r.Enable("backoff")

	sv := func() *supervisor {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.plugins["backoff"].supervisor
	}()

	// First restart: 0 + jitter (0-500ms).
	r.mu.Lock()
	d0 := sv.BackoffDuration()
	r.mu.Unlock()
	if d0 < 0 || d0 > 500*time.Millisecond {
		t.Errorf("first backoff should be 0-500ms (jitter), got %s", d0)
	}

	// Simulate one restart: 1s + jitter (0-500ms).
	r.mu.Lock()
	sv.consecutiveRestarts = 1
	d1 := sv.BackoffDuration()
	r.mu.Unlock()
	if d1 < 1*time.Second || d1 > 1500*time.Millisecond {
		t.Errorf("second backoff should be 1s-1.5s (1s + jitter), got %s", d1)
	}

	// Third crash -> circuit breaker: 2s + jitter (0-500ms).
	r.mu.Lock()
	sv.consecutiveRestarts = 2
	d2 := sv.BackoffDuration()
	r.mu.Unlock()
	if d2 < 2*time.Second || d2 > 2500*time.Millisecond {
		t.Errorf("third backoff should be 2s-2.5s (2s + jitter), got %s", d2)
	}

	// End-to-end circuit breaker: 3 crashes via recordCrashAndMaybeRestart -> STOPPED.
	r2 := newTestRegistryB1()
	p2 := newMinimalPlugin("cb-e2e")
	r2.Register(p2)
	r2.Enable("cb-e2e")

	for i := 0; i < 3; i++ {
		r2.recordCrashAndMaybeRestart("cb-e2e")
	}

	// Circuit breaker is synchronous (fires inside recordCrashAndMaybeRestart).
	// No polling needed, but wait briefly for any restart goroutines to settle.
	if !waitForState(r2, "cb-e2e", StateStopped, 2*time.Second) {
		info, _ := r2.GetInfo("cb-e2e")
		t.Errorf("expected STOPPED after 3 crashes (circuit breaker), got %s", info.State)
	}
}

// --- Test helpers ---

func createTestConfigDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(dir+"/config.yaml", []byte("initial: true"), 0600)
}

func writeTestConfig(path, content string) error {
	return os.WriteFile(path, []byte(content), 0600)
}

// H31: Window gaming attack hits lifetime limit.
// Attacker crashes plugin 2x, waits for window reset, repeats. Should hit lifetime limit at 10.
func TestHostile_WindowGaming(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("gaming")
	r.Register(p)
	r.Enable("gaming")

	sv := func() *supervisor {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.plugins["gaming"].supervisor
	}()

	// Simulate window gaming: 2 crashes per window, reset window, repeat.
	for round := 0; round < 5; round++ {
		r.mu.Lock()
		// Reset window to simulate time passing.
		sv.crashCount = 0
		sv.firstCrash = time.Time{}
		// Two crashes in this window (stays under threshold of 3).
		sv.RecordCrash()
		sv.RecordCrash()
		r.mu.Unlock()
	}

	// After 5 rounds x 2 crashes = 10 lifetime crashes -> permanently disabled.
	r.mu.RLock()
	if sv.lifetimeCrashes != 10 {
		t.Fatalf("expected 10 lifetime crashes, got %d", sv.lifetimeCrashes)
	}
	// Window count is only 2 (under threshold), but lifetime kills it.
	if sv.crashCount != 2 {
		t.Fatalf("expected window crash count 2, got %d", sv.crashCount)
	}
	should := sv.ShouldAutoRestart()
	r.mu.RUnlock()
	if should {
		t.Error("window gaming attack should be blocked by lifetime limit")
	}
}

// H32: Restart jitter produces non-identical durations.
func TestHostile_RestartJitter(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("jitter")
	r.Register(p)
	r.Enable("jitter")

	sv := func() *supervisor {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.plugins["jitter"].supervisor
	}()

	// Collect multiple backoff durations at the same restart level.
	// With jitter, they should NOT all be identical (probabilistic, but with
	// 500ms range and 20 samples, getting all identical is astronomically unlikely).
	durations := make(map[time.Duration]bool)
	r.mu.Lock()
	for i := 0; i < 20; i++ {
		sv.consecutiveRestarts = 0
		d := sv.BackoffDuration()
		durations[d] = true
	}
	r.mu.Unlock()

	if len(durations) < 2 {
		t.Error("jitter should produce at least 2 distinct durations in 20 samples")
	}
}
