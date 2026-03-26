package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// I1: Full lifecycle Register -> Enable -> verify -> Disable -> verify empty.
func TestAcceptance_FullLifecycle(t *testing.T) {
	r := newTestRegistryB1()

	p := newMinimalPlugin("lifecycle")
	p.commands = []Command{
		{Name: "test-cmd", Description: "test command"},
	}
	p.routes = []Route{
		{Method: "GET", Path: "/v1/test", Handler: noopHandler()},
	}

	// Step 1: Register
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	info, _ := r.GetInfo("lifecycle")
	if info.State != StateReady {
		t.Fatalf("expected READY after Register, got %s", info.State)
	}

	// Step 2: Enable
	if err := r.Enable("lifecycle"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	info, _ = r.GetInfo("lifecycle")
	if info.State != StateActive {
		t.Fatalf("expected ACTIVE after Enable, got %s", info.State)
	}

	// Step 3: Verify commands visible
	cmds := r.AllCommands()
	if len(cmds) != 1 || cmds[0].Name != "test-cmd" {
		t.Errorf("expected 1 command 'test-cmd', got %v", cmds)
	}

	// Step 4: Verify routes visible
	routes := r.AllRoutes()
	if len(routes) != 1 || routes[0].Path != "/v1/test" {
		t.Errorf("expected 1 route '/v1/test', got %v", routes)
	}

	// Step 5: Disable
	if err := r.Disable("lifecycle"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	info, _ = r.GetInfo("lifecycle")
	if info.State != StateStopped {
		t.Fatalf("expected STOPPED after Disable, got %s", info.State)
	}

	// Step 6: Verify commands gone
	cmds = r.AllCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands after disable, got %d", len(cmds))
	}

	// Step 7: Verify routes gone
	routes = r.AllRoutes()
	if len(routes) != 0 {
		t.Errorf("expected 0 routes after disable, got %d", len(routes))
	}
}

// I2: Protocol registration with real ServiceRegistry.
func TestAcceptance_ProtocolRegistrationWithServiceRegistry(t *testing.T) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	sr := p2pnet.NewServiceRegistry(h, nil)

	r := NewRegistry(&ContextProvider{
		ServiceRegistry: sr,
	})
	r.enableDisableCooldown = 0

	p := newMinimalPlugin("proto-test")
	p.protocols = []Protocol{
		{Name: "test-alpha", Version: "1.0.0", Handler: noopStreamHandler()},
		{Name: "test-beta", Version: "1.0.0", Handler: noopStreamHandler()},
	}

	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Enable -> protocols registered.
	if err := r.Enable("proto-test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Verify both protocols are findable.
	for _, name := range []string{"test-alpha", "test-beta"} {
		if _, ok := sr.GetService(name); !ok {
			t.Errorf("expected service %q to be registered", name)
		}
	}

	// Verify wrapHandler passes through.
	protos := r.ActiveProtocols()
	if len(protos) != 2 {
		t.Errorf("expected 2 active protocols, got %d", len(protos))
	}

	// Disable -> protocols unregistered.
	if err := r.Disable("proto-test"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	for _, name := range []string{"test-alpha", "test-beta"} {
		if _, ok := sr.GetService(name); ok {
			t.Errorf("expected service %q to be unregistered after disable", name)
		}
	}

	protos = r.ActiveProtocols()
	if len(protos) != 0 {
		t.Errorf("expected 0 active protocols after disable, got %d", len(protos))
	}
}

// I3: Config reload chain.
func TestAcceptance_ConfigReloadChain(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins", "test.io", "mock", "reload")
	os.MkdirAll(pluginDir, 0700)

	// Write initial config.
	configPath := filepath.Join(pluginDir, "config.yaml")
	os.WriteFile(configPath, []byte("key: value1"), 0600)

	r := NewRegistry(&ContextProvider{
		ConfigDir: tmpDir,
	})
	r.enableDisableCooldown = 0

	var reloadedBytes atomic.Value
	p := newMinimalPlugin("reload")
	p.id = "test.io/mock/reload"
	p.initFn = func(ctx *PluginContext) error {
		ctx.OnConfigReload(func(data []byte) {
			reloadedBytes.Store(string(data))
		})
		return nil
	}

	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Enable("reload"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Write new config and trigger reload.
	os.WriteFile(configPath, []byte("key: value2"), 0600)
	r.NotifyConfigReload()

	got, ok := reloadedBytes.Load().(string)
	if !ok || got != "key: value2" {
		t.Errorf("expected reload callback with 'key: value2', got %q", got)
	}
}

// I4: DisableAll kill switch - Stop called in reverse registration order.
func TestAcceptance_DisableAllKillSwitch(t *testing.T) {
	r := newTestRegistryB1()

	// Atomic sequence counter to track Stop call order.
	var stopSeq atomic.Int32
	stopOrder := make(map[string]int32)
	var stopMu sync.Mutex

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		name := name // capture
		p := newMinimalPlugin(name)
		p.stopFn = func() error {
			seq := stopSeq.Add(1)
			stopMu.Lock()
			stopOrder[name] = seq
			stopMu.Unlock()
			return nil
		}
		r.Register(p)
		r.Enable(name)
	}

	count, err := r.DisableAll()
	if err != nil {
		t.Fatalf("DisableAll: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 disabled, got %d", count)
	}

	for _, name := range names {
		info, _ := r.GetInfo(name)
		if info.State != StateStopped {
			t.Errorf("plugin %q expected STOPPED, got %s", name, info.State)
		}
	}

	// Verify reverse registration order: gamma(1) -> beta(2) -> alpha(3).
	stopMu.Lock()
	defer stopMu.Unlock()
	if stopOrder["gamma"] != 1 {
		t.Errorf("gamma should be stopped first (seq 1), got %d", stopOrder["gamma"])
	}
	if stopOrder["beta"] != 2 {
		t.Errorf("beta should be stopped second (seq 2), got %d", stopOrder["beta"])
	}
	if stopOrder["alpha"] != 3 {
		t.Errorf("alpha should be stopped last (seq 3), got %d", stopOrder["alpha"])
	}

	cmds := r.AllCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands after DisableAll, got %d", len(cmds))
	}
}

// I5: Re-enable produces clean state (F3 regression test).
func TestAcceptance_ReenableCleanState(t *testing.T) {
	r := newTestRegistryB1()

	p := newMinimalPlugin("reenable")
	p.commands = []Command{
		{Name: "re-cmd", Description: "test"},
	}

	r.Register(p)
	r.Enable("reenable")

	if p.startCalls.Load() != 1 {
		t.Fatalf("expected 1 Start call, got %d", p.startCalls.Load())
	}

	r.Disable("reenable")
	r.Enable("reenable")

	if p.startCalls.Load() != 2 {
		t.Errorf("expected 2 Start calls after re-enable, got %d", p.startCalls.Load())
	}

	// Verify commands work after re-enable.
	cmds := r.AllCommands()
	if len(cmds) != 1 {
		t.Errorf("expected 1 command after re-enable, got %d", len(cmds))
	}

	info, _ := r.GetInfo("reenable")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after re-enable, got %s", info.State)
	}
	if info.CrashCount != 0 {
		t.Errorf("expected 0 crash count after re-enable, got %d", info.CrashCount)
	}
}

// I6: OnNetworkReady fanout - only active plugins receive it.
func TestAcceptance_OnNetworkReadyFanout(t *testing.T) {
	origTimeout := startTimeoutDuration
	startTimeoutDuration = 2 * time.Second
	defer func() { startTimeoutDuration = origTimeout }()

	r := newTestRegistryB1()

	a := newMinimalPlugin("fanout-a")
	b := newMinimalPlugin("fanout-b")
	c := newMinimalPlugin("fanout-c")

	r.Register(a)
	r.Register(b)
	r.Register(c)

	r.Enable("fanout-a")
	r.Enable("fanout-b")
	// c NOT enabled - stays READY.

	r.NotifyNetworkReady()

	if a.networkReadyCalls.Load() != 1 {
		t.Errorf("plugin A: expected 1 OnNetworkReady call, got %d", a.networkReadyCalls.Load())
	}
	if b.networkReadyCalls.Load() != 1 {
		t.Errorf("plugin B: expected 1 OnNetworkReady call, got %d", b.networkReadyCalls.Load())
	}
	if c.networkReadyCalls.Load() != 0 {
		t.Errorf("plugin C (disabled): expected 0 OnNetworkReady calls, got %d", c.networkReadyCalls.Load())
	}
}

// I7: Config-driven startup.
func TestAcceptance_ConfigDrivenStartup(t *testing.T) {
	r := newTestRegistryB1()

	a := newMinimalPlugin("cfg-a")
	b := newMinimalPlugin("cfg-b")
	c := newMinimalPlugin("cfg-c")
	a.commands = []Command{{Name: "cmd-a"}}
	b.commands = []Command{{Name: "cmd-b"}}
	c.commands = []Command{{Name: "cmd-c"}}

	r.Register(a)
	r.Register(b)
	r.Register(c)

	r.ApplyConfig(map[string]bool{
		"cfg-a": true,
		"cfg-b": false,
		"cfg-c": true,
	})

	// A and C should be ACTIVE, B should be STOPPED.
	infoA, _ := r.GetInfo("cfg-a")
	infoB, _ := r.GetInfo("cfg-b")
	infoC, _ := r.GetInfo("cfg-c")

	if infoA.State != StateActive {
		t.Errorf("cfg-a: expected ACTIVE, got %s", infoA.State)
	}
	if infoB.State != StateStopped {
		t.Errorf("cfg-b: expected STOPPED, got %s", infoB.State)
	}
	if infoC.State != StateActive {
		t.Errorf("cfg-c: expected ACTIVE, got %s", infoC.State)
	}

	// B's commands should be absent.
	cmds := r.AllCommands()
	for _, cmd := range cmds {
		if cmd.Name == "cmd-b" {
			t.Error("cmd-b should not be visible when cfg-b is disabled")
		}
	}

	// Enable B -> becomes ACTIVE.
	r.Enable("cfg-b")
	infoB, _ = r.GetInfo("cfg-b")
	if infoB.State != StateActive {
		t.Errorf("cfg-b: expected ACTIVE after enable, got %s", infoB.State)
	}
}

// I8: HTTP route gating - routes return 404 when plugin disabled.
func TestAcceptance_HTTPRouteGating(t *testing.T) {
	r := newTestRegistryB1()

	var handlerCalled atomic.Bool
	p := newMinimalPlugin("http-gate")
	p.routes = []Route{
		{Method: "POST", Path: "/v1/test", Handler: func(w http.ResponseWriter, r *http.Request) {
			handlerCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		}},
	}

	r.Register(p)
	r.Enable("http-gate")

	// Route should be active.
	if !r.IsRouteActive("POST", "/v1/test") {
		t.Error("expected route active when plugin enabled")
	}

	// Simulate HTTP request.
	req := httptest.NewRequest("POST", "/v1/test", nil)
	w := httptest.NewRecorder()
	routes := r.AllRoutes()
	if len(routes) == 1 {
		routes[0].Handler(w, req)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Disable -> route should be inactive.
	r.Disable("http-gate")
	if r.IsRouteActive("POST", "/v1/test") {
		t.Error("expected route inactive when plugin disabled")
	}

	routes = r.AllRoutes()
	if len(routes) != 0 {
		t.Error("expected 0 routes after disable")
	}
}

// I9: CLI command execution.
func TestAcceptance_CLICommandExecution(t *testing.T) {
	r := newTestRegistryB1()

	var receivedArgs []string
	p := newMinimalPlugin("cli-exec")
	p.commands = []Command{
		{Name: "test-cmd", Description: "test", Run: func(args []string) {
			receivedArgs = args
		}},
	}

	r.Register(p)
	r.Enable("cli-exec")

	cmds := r.AllCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.Name == "test-cmd" {
			found = true
			cmd.Run([]string{"arg1", "arg2"})
			break
		}
	}

	if !found {
		t.Fatal("test-cmd not found in AllCommands")
	}
	if len(receivedArgs) != 2 || receivedArgs[0] != "arg1" || receivedArgs[1] != "arg2" {
		t.Errorf("expected [arg1, arg2], got %v", receivedArgs)
	}
}

// I10: Multi-plugin namespace - disabling one doesn't affect another.
func TestAcceptance_MultiPluginNamespace(t *testing.T) {
	r := newTestRegistryB1()

	a := newMinimalPlugin("ns-a")
	a.commands = []Command{
		{Name: "send"}, {Name: "list"},
	}
	b := newMinimalPlugin("ns-b")
	b.commands = []Command{
		{Name: "browse"}, {Name: "share"},
	}

	r.Register(a)
	r.Register(b)
	r.Enable("ns-a")
	r.Enable("ns-b")

	if len(r.AllCommands()) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(r.AllCommands()))
	}

	// Disable A -> only B's commands remain.
	r.Disable("ns-a")
	cmds := r.AllCommands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands after disabling A, got %d", len(cmds))
	}
	for _, cmd := range cmds {
		if cmd.Name == "send" || cmd.Name == "list" {
			t.Errorf("A's command %q should not be visible", cmd.Name)
		}
	}

	// Re-enable A -> all 4 back.
	r.Enable("ns-a")
	if len(r.AllCommands()) != 4 {
		t.Errorf("expected 4 commands after re-enabling A, got %d", len(r.AllCommands()))
	}
}

// I11: Progressive context cancellation.
func TestAcceptance_ProgressiveContextCancel(t *testing.T) {
	r := newTestRegistryB1()

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("cancel-%d", i)
		var startCtx context.Context
		p := newMinimalPlugin(name)
		p.startFn = func(ctx context.Context) error {
			startCtx = ctx
			return nil
		}

		r.Register(p)
		r.Enable(name)

		// Verify context is live.
		if startCtx == nil {
			t.Fatalf("iteration %d: Start context was nil", i)
		}
		if startCtx.Err() != nil {
			t.Fatalf("iteration %d: Start context already cancelled", i)
		}

		// Disable cancels via stop, not context.
		r.Disable(name)

		// Verify no stale commands.
		cmds := r.AllCommands()
		if len(cmds) != 0 {
			t.Errorf("iteration %d: expected 0 commands after cancel-disable, got %d", i, len(cmds))
		}
	}
}

// I12: Checkpoint -> Restore cycle across auto-restart via supervisor.
func TestAcceptance_CheckpointRestoreCycle(t *testing.T) {
	r := newTestRegistryB1()

	var restoredData atomic.Value
	p := newCheckpointPlugin("cp-cycle", []byte(`{"state":"active"}`))
	p.restoreFn = func(data []byte) error {
		restoredData.Store(string(data))
		return nil
	}

	r.Register(p)
	r.Enable("cp-cycle")

	// Verify Checkpoint returns expected data.
	data, err := p.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if string(data) != `{"state":"active"}` {
		t.Errorf("expected checkpoint data, got %q", string(data))
	}

	// End-to-end: handler crash -> supervisor Checkpoint -> Disable -> Enable -> Restore.
	r.recordCrashAndMaybeRestart("cp-cycle")

	if !waitForState(r, "cp-cycle", StateActive, 2*time.Second) {
		info, _ := r.GetInfo("cp-cycle")
		t.Fatalf("expected ACTIVE after supervisor restart, got %s", info.State)
	}

	// Verify Restore was called with the checkpoint data.
	got, ok := restoredData.Load().(string)
	if !ok || got != `{"state":"active"}` {
		t.Errorf("expected restored data via supervisor, got %q", got)
	}
}

// I13: ErrSkipCheckpoint is honored.
func TestAcceptance_CheckpointSkip(t *testing.T) {
	r := newTestRegistryB1()

	p := newCheckpointPlugin("cp-skip", nil) // nil data -> ErrSkipCheckpoint

	r.Register(p)
	r.Enable("cp-skip")

	_, err := p.Checkpoint()
	if err != ErrSkipCheckpoint {
		t.Errorf("expected ErrSkipCheckpoint, got %v", err)
	}
}

// I14: Non-Checkpointer plugin restarts work fine (fresh Start).
func TestAcceptance_NoCheckpointerStatelessRestart(t *testing.T) {
	r := newTestRegistryB1()

	p := newMinimalPlugin("no-cp") // does NOT implement Checkpointer

	r.Register(p)
	r.Enable("no-cp")
	r.Disable("no-cp")
	r.Enable("no-cp")

	if p.startCalls.Load() != 2 {
		t.Errorf("expected 2 Start calls (stateless restart), got %d", p.startCalls.Load())
	}

	// Verify minimal plugin is not a Checkpointer.
	// testPlugin has Checkpoint/Restore methods but doesn't satisfy the
	// Checkpointer interface unless wrapped in testCheckpointerPlugin.
	if _, ok := interface{}(p).(Checkpointer); ok {
		t.Error("minimal plugin should NOT satisfy Checkpointer interface")
	}

	info, _ := r.GetInfo("no-cp")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after stateless re-enable, got %s", info.State)
	}
}

// Ensure test compiles: verify key types exist.
var _ Plugin = (*testPlugin)(nil)
var _ StatusContributor = (*testPlugin)(nil)
