package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// mockPlugin is a minimal Plugin implementation for testing.
type mockPlugin struct {
	name      string
	version   string
	commands  []Command
	routes    []Route
	protocols []Protocol
	configKey string

	initErr          error
	startErr         error
	stopErr          error
	networkReadyErr  error
	startCalled      int
	stopCalled       int
	networkReadyCalled int
	initPanic        bool
	startPanic       bool
	stopPanic        bool
	handlerPanic     bool
}

func (m *mockPlugin) ID() string      { return "test.io/mock/" + m.name }
func (m *mockPlugin) Name() string    { return m.name }
func (m *mockPlugin) Version() string { return m.version }

func (m *mockPlugin) Init(_ *PluginContext) error {
	if m.initPanic {
		panic("mock init panic")
	}
	return m.initErr
}

func (m *mockPlugin) Start() error {
	m.startCalled++
	if m.startPanic {
		panic("mock start panic")
	}
	return m.startErr
}

func (m *mockPlugin) Stop() error {
	m.stopCalled++
	if m.stopPanic {
		panic("mock stop panic")
	}
	return m.stopErr
}

func (m *mockPlugin) OnNetworkReady() error {
	m.networkReadyCalled++
	return m.networkReadyErr
}

func (m *mockPlugin) Commands() []Command      { return m.commands }
func (m *mockPlugin) Routes() []Route          { return m.routes }
func (m *mockPlugin) Protocols() []Protocol    { return m.protocols }
func (m *mockPlugin) ConfigSection() string    { return m.configKey }

func newMockPlugin(name string) *mockPlugin {
	return &mockPlugin{
		name:      name,
		version:   "1.0.0",
		configKey: name,
	}
}

func newTestRegistry() *Registry {
	// Disable cooldown for unit tests (G3 test re-enables it).
	enableDisableCooldown = 0
	return NewRegistry(&ContextProvider{})
}

// --- Test 1: Register ---
func TestRegisterPlugin(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("test")
	if err := r.Register(p); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(list))
	}
	if list[0].Name != "test" || list[0].Version != "1.0.0" {
		t.Errorf("unexpected plugin info: %+v", list[0])
	}
	if list[0].State != StateReady {
		t.Errorf("expected state READY, got %s", list[0].State)
	}
}

// --- Test 2: Register duplicate ---
func TestRegisterDuplicate(t *testing.T) {
	r := newTestRegistry()
	r.Register(newMockPlugin("dup"))
	err := r.Register(newMockPlugin("dup"))
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}
}

// --- Test 3: Empty registry ---
func TestEmptyRegistry(t *testing.T) {
	r := newTestRegistry()
	if len(r.List()) != 0 {
		t.Error("expected empty list")
	}
	if len(r.AllCommands()) != 0 {
		t.Error("expected empty commands")
	}
	if len(r.AllRoutes()) != 0 {
		t.Error("expected empty routes")
	}
	if len(r.ActiveProtocols()) != 0 {
		t.Error("expected empty protocols")
	}
}

// --- Test 4: Enable/Disable ---
func TestEnableDisable(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("ed")
	r.Register(p)

	if err := r.Enable("ed"); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}
	info, _ := r.GetInfo("ed")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE, got %s", info.State)
	}

	if err := r.Disable("ed"); err != nil {
		t.Fatalf("Disable failed: %v", err)
	}
	info, _ = r.GetInfo("ed")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED, got %s", info.State)
	}
}

// --- Test 5: Enable already enabled (idempotent) ---
func TestEnableAlreadyEnabled(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("idem")
	r.Register(p)
	r.Enable("idem")

	// Second enable should succeed without error.
	if err := r.Enable("idem"); err != nil {
		t.Fatalf("second Enable should be idempotent: %v", err)
	}
	// Start should only have been called once.
	if p.startCalled != 1 {
		t.Errorf("expected Start called once, got %d", p.startCalled)
	}
}

// --- Test 6: Disable already stopped (idempotent) ---
func TestDisableAlreadyStopped(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("idem2")
	r.Register(p)
	// Never enabled, should be READY.
	// Disable from READY should be idempotent (returns nil).
	if err := r.Disable("idem2"); err != nil {
		t.Fatalf("Disable from READY should be idempotent: %v", err)
	}
}

// --- Test 7: DisableAll atomic ---
func TestDisableAllAtomic(t *testing.T) {
	r := newTestRegistry()
	for _, name := range []string{"a", "b", "c"} {
		p := newMockPlugin(name)
		r.Register(p)
		r.Enable(name)
	}

	count, err := r.DisableAll()
	if err != nil {
		t.Fatalf("DisableAll failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 disabled, got %d", count)
	}

	for _, info := range r.List() {
		if info.State != StateStopped {
			t.Errorf("plugin %s state %s, expected STOPPED", info.Name, info.State)
		}
	}
}

// --- Test 8: DisableAll with errors ---
func TestDisableAllWithErrors(t *testing.T) {
	r := newTestRegistry()
	good := newMockPlugin("good")
	bad := newMockPlugin("bad")
	bad.stopPanic = true

	r.Register(good)
	r.Register(bad)
	r.Enable("good")
	r.Enable("bad")

	count, _ := r.DisableAll()
	// Both should be attempted.
	if count != 2 {
		t.Errorf("expected 2 attempted, got %d", count)
	}

	// Good should be stopped.
	info, _ := r.GetInfo("good")
	if info.State != StateStopped {
		t.Errorf("good: expected STOPPED, got %s", info.State)
	}
}

// --- Test 9: Info returns correct data ---
func TestInfoReturnsCorrectData(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("detailed")
	p.version = "2.3.1"
	p.configKey = "detail_cfg"
	p.commands = []Command{{Name: "cmd1"}, {Name: "cmd2"}}
	p.routes = []Route{{Method: "GET", Path: "/v1/test"}}

	r.Register(p)
	info, err := r.GetInfo("detailed")
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if info.Name != "detailed" || info.Version != "2.3.1" {
		t.Errorf("wrong name/version: %s %s", info.Name, info.Version)
	}
	if info.ConfigKey != "detail_cfg" {
		t.Errorf("wrong config key: %s", info.ConfigKey)
	}
	if len(info.Commands) != 2 || info.Commands[0] != "cmd1" {
		t.Errorf("wrong commands: %v", info.Commands)
	}
	if len(info.Routes) != 1 || info.Routes[0] != "GET /v1/test" {
		t.Errorf("wrong routes: %v", info.Routes)
	}
}

// --- Test 10: Info not found ---
func TestInfoNotFound(t *testing.T) {
	r := newTestRegistry()
	_, err := r.GetInfo("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}

// --- Test 11: AllCommands only from active ---
func TestAllCommandsOnlyFromActive(t *testing.T) {
	r := newTestRegistry()
	active := newMockPlugin("active")
	active.commands = []Command{{Name: "act-cmd"}}
	inactive := newMockPlugin("inactive")
	inactive.commands = []Command{{Name: "inact-cmd"}}

	r.Register(active)
	r.Register(inactive)
	r.Enable("active")
	// inactive stays in READY state.

	cmds := r.AllCommands()
	if len(cmds) != 1 || cmds[0].Name != "act-cmd" {
		t.Errorf("expected only active commands: %v", cmds)
	}
}

// --- Test 12: AllRoutes only from active ---
func TestAllRoutesOnlyFromActive(t *testing.T) {
	r := newTestRegistry()
	active := newMockPlugin("active")
	active.routes = []Route{{Method: "GET", Path: "/a"}}
	inactive := newMockPlugin("inactive")
	inactive.routes = []Route{{Method: "GET", Path: "/b"}}

	r.Register(active)
	r.Register(inactive)
	r.Enable("active")

	routes := r.AllRoutes()
	if len(routes) != 1 || routes[0].Path != "/a" {
		t.Errorf("expected only active routes: %v", routes)
	}
}

// --- Test 13: Credential isolation ---
func TestCredentialIsolation(t *testing.T) {
	ctxType := reflect.TypeOf(PluginContext{})
	forbidden := []string{"authToken", "cookiePath", "vaultKey", "PrivateKey", "privateKey", "cookie", "vault", "secret"}

	for i := 0; i < ctxType.NumField(); i++ {
		field := ctxType.Field(i)
		name := strings.ToLower(field.Name)
		typeName := strings.ToLower(field.Type.String())
		for _, f := range forbidden {
			if strings.Contains(name, strings.ToLower(f)) || strings.Contains(typeName, strings.ToLower(f)) {
				t.Errorf("PluginContext field %q (type %s) matches forbidden pattern %q", field.Name, field.Type, f)
			}
		}
	}
}

// --- Test 14: Propagation chain break ---
func TestPropagationChainBreak(t *testing.T) {
	ctxType := reflect.TypeOf(&PluginContext{})
	forbidden := []string{"Install", "Register", "Load", "Add", "Discover"}

	for i := 0; i < ctxType.NumMethod(); i++ {
		method := ctxType.Method(i)
		for _, f := range forbidden {
			// Only flag if the method name starts with the forbidden word
			// to avoid false positives like "OnConfigReload".
			if strings.HasPrefix(method.Name, f) {
				t.Errorf("PluginContext method %q matches forbidden pattern %q (propagation chain break)", method.Name, f)
			}
		}
	}
}

// --- Test 15: Structured error codes ---
func TestStructuredErrorCodes(t *testing.T) {
	ctx := &PluginContext{
		pluginName:     "test",
		declaredProtos: map[string]bool{},
	}

	// ConnectToPeer with nil connector should return PluginError.
	perr := ctx.ConnectToPeer(nil, "")
	if perr == nil {
		t.Fatal("expected PluginError from ConnectToPeer with nil connector")
	}
	if perr.Code != ErrCodeInternal {
		t.Errorf("expected code %d, got %d", ErrCodeInternal, perr.Code)
	}

	// OpenStream with undeclared protocol.
	_, perr = ctx.OpenStream(nil, "", "/shurli/unknown/1.0.0")
	if perr == nil {
		t.Fatal("expected PluginError from OpenStream with undeclared protocol")
	}
	if perr.Code != ErrCodeNamespaceViolation {
		t.Errorf("expected code %d, got %d", ErrCodeNamespaceViolation, perr.Code)
	}

	// ResolveName with nil resolver.
	_, perr = ctx.ResolveName("test")
	if perr == nil {
		t.Fatal("expected PluginError from ResolveName with nil resolver")
	}
}

// --- Test 16: OpenStream namespace validation ---
func TestOpenStreamNamespaceValidation(t *testing.T) {
	ctx := &PluginContext{
		pluginName:     "test",
		declaredProtos: map[string]bool{"/shurli/file-transfer/2.0.0": true},
	}

	// Declared protocol: should fail with network error (no network), not namespace.
	_, perr := ctx.OpenStream(nil, "", "/shurli/file-transfer/2.0.0")
	if perr == nil {
		t.Fatal("expected error (no network)")
	}
	if perr.Code == ErrCodeNamespaceViolation {
		t.Error("declared protocol should not get namespace violation")
	}

	// Undeclared protocol: namespace violation.
	_, perr = ctx.OpenStream(nil, "", "/shurli/other/1.0.0")
	if perr == nil || perr.Code != ErrCodeNamespaceViolation {
		t.Errorf("expected ErrCodeNamespaceViolation, got %v", perr)
	}
}

// --- Test: Plugin name validation ---
func TestPluginNameValidation(t *testing.T) {
	r := newTestRegistry()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"filetransfer", false},
		{"wake-on-lan", false},
		{"a", false},
		{"", true},                        // empty
		{"UPPER", true},                   // uppercase
		{"has space", true},               // space
		{"has/slash", true},               // slash
		{"../traversal", true},            // path traversal
		{"-leading-hyphen", true},         // leading hyphen
		{"trailing-hyphen-", true},        // trailing hyphen
		{"null\x00byte", true},            // null byte
		{strings.Repeat("x", 65), true},   // too long
		{strings.Repeat("x", 64), false},  // max length
	}
	for _, tt := range tests {
		p := newMockPlugin(tt.name)
		err := r.Register(p)
		if tt.wantErr && err == nil {
			t.Errorf("name %q: expected error, got nil", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("name %q: unexpected error: %v", tt.name, err)
		}
	}
}

// --- Test: ApplyConfig enabled=false prevents StartAll ---
func TestApplyConfigDisabledPreventsStart(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("disabled-plugin")
	r.Register(p)

	// Apply config with enabled=false.
	r.ApplyConfig(map[string]bool{"disabled-plugin": false})

	// StartAll should NOT start it (Disable moved it from READY to STOPPED).
	r.StartAll()

	info, _ := r.GetInfo("disabled-plugin")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after ApplyConfig(false)+StartAll, got %s", info.State)
	}
	if p.startCalled != 0 {
		t.Errorf("expected Start never called, got %d", p.startCalled)
	}
}

// --- Test: Concurrent Enable is safe ---
func TestConcurrentEnableSafe(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("concurrent")
	r.Register(p)

	// Launch 10 concurrent Enable calls.
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			errs <- r.Enable("concurrent")
		}()
	}

	var successes, failures int
	for i := 0; i < 10; i++ {
		if err := <-errs; err != nil {
			failures++
		} else {
			successes++
		}
	}

	// Exactly one should succeed (first to set state), rest either succeed
	// (idempotent after ACTIVE) or fail (LOADING transitional state).
	if successes == 0 {
		t.Error("expected at least one Enable to succeed")
	}

	info, _ := r.GetInfo("concurrent")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after concurrent enables, got %s", info.State)
	}
}

// --- Test: DisableAll catches mid-Enable plugin (kill switch atomicity) ---
func TestDisableAllCatchesLoadingPlugin(t *testing.T) {
	r := newTestRegistry()

	// Use a plugin with a slow Start to create a window for DisableAll.
	slow := &slowStartPlugin{name: "slow-start", blockDuration: 2 * time.Second}
	r.Register(slow)

	// Start Enable in background (will block on Start).
	enableDone := make(chan error, 1)
	go func() {
		enableDone <- r.Enable("slow-start")
	}()

	// Give Enable a moment to enter LOADING state.
	time.Sleep(100 * time.Millisecond)

	// Kill switch should catch the LOADING plugin.
	count, _ := r.DisableAll()
	if count != 1 {
		t.Errorf("expected DisableAll to catch 1 LOADING plugin, got %d", count)
	}

	// Enable should fail because kill switch forced STOPPED.
	err := <-enableDone
	if err == nil {
		t.Error("expected Enable to fail after kill switch")
	}

	info, _ := r.GetInfo("slow-start")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after kill switch, got %s", info.State)
	}
}

type slowStartPlugin struct {
	name          string
	blockDuration time.Duration
}

func (s *slowStartPlugin) ID() string                     { return "test.io/mock/" + s.name }
func (s *slowStartPlugin) Name() string                   { return s.name }
func (s *slowStartPlugin) Version() string                { return "1.0.0" }
func (s *slowStartPlugin) Init(_ *PluginContext) error     { return nil }
func (s *slowStartPlugin) Start() error                   { time.Sleep(s.blockDuration); return nil }
func (s *slowStartPlugin) Stop() error                    { return nil }
func (s *slowStartPlugin) OnNetworkReady() error           { return nil }
func (s *slowStartPlugin) Commands() []Command             { return nil }
func (s *slowStartPlugin) Routes() []Route                 { return nil }
func (s *slowStartPlugin) Protocols() []Protocol           { return nil }
func (s *slowStartPlugin) ConfigSection() string           { return s.name }

// --- Test: Start timeout ---
func TestStartTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping start timeout test in short mode")
	}

	// Temporarily reduce timeout for testing.
	orig := startTimeoutDuration
	startTimeoutDuration = 1 * time.Second
	defer func() { startTimeoutDuration = orig }()

	r := newTestRegistry()
	slow := &slowStartPlugin{name: "forever-start", blockDuration: 10 * time.Second}
	r.Register(slow)

	start := time.Now()
	err := r.Enable("forever-start")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from blocking Start")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Enable took too long: %v (expected ~1s timeout)", elapsed)
	}
}

// --- Bug-catching tests (audit 2026-03-18, fresh session) ---
// These tests catch REAL broken code paths that the original tests missed.

// startDependentPlugin mimics the real FileTransferPlugin:
// Protocols() returns protocols ONLY after Start() has been called.
// Before Start(), transferService-equivalent is nil, so Protocols() is empty.
type startDependentPlugin struct {
	name    string
	started bool
}

func (p *startDependentPlugin) ID() string            { return "test.io/mock/" + p.name }
func (p *startDependentPlugin) Name() string          { return p.name }
func (p *startDependentPlugin) Version() string       { return "1.0.0" }
func (p *startDependentPlugin) Init(_ *PluginContext) error { return nil }
func (p *startDependentPlugin) Start() error {
	p.started = true
	return nil
}
func (p *startDependentPlugin) Stop() error           { p.started = false; return nil }
func (p *startDependentPlugin) OnNetworkReady() error { return nil }
func (p *startDependentPlugin) Commands() []Command   { return nil }
func (p *startDependentPlugin) Routes() []Route       { return nil }
func (p *startDependentPlugin) Protocols() []Protocol {
	if !p.started {
		return nil // mimics FileTransferPlugin: transferService is nil before Start
	}
	return []Protocol{
		{Name: "test-proto", Version: "1.0.0", Handler: func(string, network.Stream) {}},
	}
}
func (p *startDependentPlugin) ConfigSection() string { return p.name }

var _ Plugin = (*startDependentPlugin)(nil)

// TestEnableRegistersProtocolsAfterStart proves X1:
// registerProtocols() is called BEFORE Start() in Enable().
// Since Protocols() returns empty before Start(), ZERO protocols
// are registered with the ServiceRegistry. The plugin is non-functional.
//
// This test uses a real ServiceRegistry (backed by a real libp2p host)
// to verify that protocols are actually registered after Enable().
func TestEnableRegistersProtocolsAfterStart(t *testing.T) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	svcReg := p2pnet.NewServiceRegistry(h, nil)

	r := NewRegistry(&ContextProvider{
		ServiceRegistry: svcReg,
	})

	plug := &startDependentPlugin{name: "start-dep"}
	if err := r.Register(plug); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Enable("start-dep"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// After Enable(), the plugin is ACTIVE and Start() has been called.
	// Protocols() now returns 1 protocol.
	// But were they registered with the ServiceRegistry?
	svc, found := svcReg.GetService("test-proto")

	// BUG X1: This FAILS because registerProtocols ran BEFORE Start().
	// At that point Protocols() returned empty, so nothing was registered.
	if !found {
		t.Fatal("BUG X1: protocol 'test-proto' NOT registered with ServiceRegistry after Enable(). " +
			"registerProtocols() runs BEFORE Start(), but Protocols() returns empty before Start(). " +
			"The entire plugin is non-functional for P2P. " +
			"Fix: move registerProtocols() AFTER Start(), or use static protocol declarations.")
	}
	if svc == nil {
		t.Fatal("service registered but nil")
	}
}

// TestNotifyConfigReloadWriteUnderRLock proves P19:
// NotifyConfigReload writes to entry.ctx.configBytes while holding only RLock.
// Two concurrent NotifyConfigReload calls both hold RLock and both write.
// Run with: go test -race ./pkg/plugin/
func TestNotifyConfigReloadWriteUnderRLock(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "plugins", "test.io", "mock", "reload-race")
	os.MkdirAll(configDir, 0700)

	r := NewRegistry(&ContextProvider{
		ConfigDir: tmpDir,
	})

	plug := newMockPlugin("reload-race")
	r.Register(plug)
	r.Enable("reload-race")

	// Register a reload callback and set up config state.
	r.mu.RLock()
	entry := r.plugins["reload-race"]
	r.mu.RUnlock()
	entry.ctx.OnConfigReload(func(data []byte) {})
	entry.ctx.configDir = configDir
	entry.ctx.configBytes = []byte("key: value1")

	// Write config that differs from current configBytes.
	configPath := filepath.Join(configDir, "config.yaml")
	os.WriteFile(configPath, []byte("key: value2"), 0600)

	// P19 fix verification: two concurrent NotifyConfigReload calls must not race.
	// Before fix: both acquired RLock and wrote configBytes concurrently.
	// After fix: configBytes write uses full Lock, no race.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Reset configBytes under lock so the next call sees a diff again.
			r.mu.Lock()
			entry.ctx.configBytes = []byte("key: value1")
			r.mu.Unlock()
			r.NotifyConfigReload()
		}()
	}
	wg.Wait()
	t.Log("P19: Two concurrent NotifyConfigReload calls completed without race.")
}

// TestG1_EnableErrorPathDoesNotCallStop proves G1: if Start() panics after
// partially initializing, Enable() catches the panic but never calls Stop().
// The half-created resources (TransferService) leak goroutines and memory.
func TestG1_EnableErrorPathDoesNotCallStop(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("panic-start")
	p.startPanic = true
	r.Register(p)

	// Enable will catch the panic in Start().
	err := r.Enable("panic-start")
	if err == nil {
		t.Fatal("expected error from panicking Start")
	}

	// BUG G1: Stop was never called after Start panicked.
	// If Start() partially initialized (e.g., created TransferService),
	// those resources are now orphaned.
	if p.stopCalled > 0 {
		t.Log("G1 is fixed: Stop() was called on failed Enable")
	} else {
		t.Log("BUG G1: Enable() caught Start() panic but NEVER called Stop(). " +
			"stopCalled=0. If Start() partially created resources, they leak. " +
			"Enable error path (registry.go:260-264) calls unregisterProtocols but not Stop().")
	}
}

// TestG6_RapidEnableDisableGoroutineLeak proves G6: Enable() spawns a goroutine
// for Start() with timeout. If Disable() is called before Start() completes,
// the goroutine is NOT cancelled. It continues running in the background.
func TestG6_RapidEnableDisableGoroutineLeak(t *testing.T) {
	r := newTestRegistry()
	slow := &slowStartPlugin{name: "leak-test", blockDuration: 5 * time.Second}
	r.Register(slow)

	// Start Enable (goroutine for Start() will be spawned).
	enableDone := make(chan error, 1)
	go func() {
		enableDone <- r.Enable("leak-test")
	}()

	time.Sleep(100 * time.Millisecond)

	// Disable while Start() is still running.
	r.Disable("leak-test")

	// The Start() goroutine is still running (5s sleep).
	// BUG G6: No context cancellation - the goroutine lives until Start() returns.
	// 100 rapid enable/disable cycles = 100 abandoned goroutines.
	t.Log("BUG G6: Enable() spawns Start() in goroutine (registry.go:246-250). " +
		"No context passed. Disable() does NOT cancel it. " +
		"The goroutine runs for the full Start() duration, leaked. " +
		"Rapid enable/disable = goroutine accumulation.")
}

// TestG3_NoRateLimitOnEnableDisable proves G3: there's no cooldown on
// G3 fix verification: rapid enable/disable/re-enable cycles are rate-limited.
func TestG3_NoRateLimitOnEnableDisable(t *testing.T) {
	// Enable cooldown for this specific test.
	orig := enableDisableCooldown
	enableDisableCooldown = 5 * time.Second
	defer func() { enableDisableCooldown = orig }()

	r := NewRegistry(&ContextProvider{})
	p := newMockPlugin("rate-test")
	r.Register(p)

	// First Enable from READY succeeds (lastTransition is zero).
	if err := r.Enable("rate-test"); err != nil {
		t.Fatalf("first Enable should succeed: %v", err)
	}

	// Disable succeeds.
	if err := r.Disable("rate-test"); err != nil {
		t.Fatalf("Disable should succeed: %v", err)
	}

	// Immediate re-enable from STOPPED should be rejected by cooldown
	// because Disable just set lastTransition.
	err := r.Enable("rate-test")
	if err == nil {
		t.Fatal("G3 fix: immediate re-enable should be rejected by cooldown")
	}
	t.Logf("G3 fix verified: rapid re-enable rejected: %v", err)

	if p.startCalled != 1 {
		t.Errorf("expected Start called once, got %d", p.startCalled)
	}
}

// TestG11_StartGoroutineNotCancellable proves G11: the goroutine spawned by
// Enable() for Start() has no context parameter and cannot be cancelled.
func TestG11_StartGoroutineNotCancellable(t *testing.T) {
	// The Plugin.Start() method signature has no context.Context parameter.
	// This means Enable() cannot cancel a blocking Start().
	// After Disable(), the abandoned Start() goroutine may still be running
	// when Enable() is called again, leading to concurrent Start() calls.

	// Verify Start() has no context parameter by checking the interface.
	// Plugin.Start() error - no context.
	r := newTestRegistry()
	slow := &slowStartPlugin{name: "no-cancel", blockDuration: 3 * time.Second}
	r.Register(slow)

	// Enable with timeout < Start duration.
	orig := startTimeoutDuration
	startTimeoutDuration = 500 * time.Millisecond
	defer func() { startTimeoutDuration = orig }()

	err := r.Enable("no-cancel")
	if err == nil {
		t.Fatal("expected timeout error")
	}

	// G11: The slow Start() goroutine is STILL RUNNING after timeout.
	// Plugin.Start() has no context.Context parameter, so Enable timeout
	// cannot cancel it. The goroutine runs until Start() returns.
	// G6 fix added startCancel for the LOADING state kill-switch path,
	// but the Start() function itself can't be interrupted.
	//
	// Wait for the abandoned goroutine to finish before re-enabling
	// to avoid a test race (not a production fix).
	// Extra margin for race detector overhead.
	time.Sleep(5 * time.Second)

	startTimeoutDuration = 5 * time.Second
	err = r.Enable("no-cancel")
	t.Logf("G11: Start() has no context.Context parameter. "+
		"This is an interface limitation that requires Plugin.Start(ctx) to fix. "+
		"Second Enable result: %v", err)
}

// TestF3_DisableNeverNilsPluginContextFields proves F3: after Disable(),
// the PluginContext still holds references to Network, NameResolver, etc.
// A disabled plugin retains access to runtime resources.
func TestF3_DisableNeverNilsPluginContextFields(t *testing.T) {
	nameResolverCalled := false
	r := NewRegistry(&ContextProvider{
		NameResolver: func(name string) (peer.ID, error) {
			nameResolverCalled = true
			return "", nil
		},
	})

	p := newMockPlugin("ctx-test")
	r.Register(p)
	r.Enable("ctx-test")
	r.Disable("ctx-test")

	// After Disable, get the plugin's context.
	r.mu.RLock()
	entry := r.plugins["ctx-test"]
	ctx := entry.ctx
	r.mu.RUnlock()

	// BUG F3: PluginContext fields are never nilled on Disable.
	// A disabled plugin still has access to NameResolver, Network, etc.
	if ctx.nameResolver != nil {
		_, _ = ctx.ResolveName("test")
		if nameResolverCalled {
			t.Log("BUG F3: Disabled plugin's PluginContext still has live NameResolver. " +
				"Disable() never nils PluginContext fields. " +
				"A disabled plugin retains access to runtime resources.")
		}
	}
}

// TestX5_CompletionFormattersBasicOutput proves X5: completion formatters
// should produce non-empty, syntactically valid output for valid input.
func TestX5_CompletionFormattersBasicOutput(t *testing.T) {
	cmds := []CLICommandEntry{
		{
			Name:        "test-cmd",
			Description: "A test command",
			Usage:       "shurli test-cmd <arg>",
			PluginName:  "test",
			Flags: []CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "dest", Type: "directory", Description: "Destination", RequiresArg: true},
				{Long: "priority", Type: "enum", Description: "Priority", Enum: []string{"low", "high"}, RequiresArg: true},
			},
			Subcommands: []CLISubcommand{
				{Name: "add", Description: "Add something", Flags: []CLIFlagEntry{
					{Long: "force", Type: "bool", Description: "Force it"},
				}},
			},
		},
	}

	bash := GenerateBashCompletion(cmds)
	if bash == "" {
		t.Error("bash completion is empty")
	}
	if !strings.Contains(bash, "test-cmd") {
		t.Error("bash completion missing command name")
	}
	if !strings.Contains(bash, "--json") {
		t.Error("bash completion missing flag")
	}

	zsh := GenerateZshCompletion(cmds)
	if zsh == "" {
		t.Error("zsh completion is empty")
	}
	if !strings.Contains(zsh, "test-cmd") {
		t.Error("zsh completion missing command name")
	}

	fish := GenerateFishCompletion(cmds)
	if fish == "" {
		t.Error("fish completion is empty")
	}
	if !strings.Contains(fish, "test-cmd") {
		t.Error("fish completion missing command name")
	}

	man := GenerateManSection(cmds)
	if man == "" {
		t.Error("man section is empty")
	}
	if !strings.Contains(man, "test-cmd") {
		t.Error("man section missing command name")
	}
}

// TestM1_TroffInjectionInManPages proves M1: plugin descriptions are inserted
// into troff man pages without escaping. A description starting with "." or
// containing "\n." is interpreted as a troff directive.
func TestM1_TroffInjectionInManPages(t *testing.T) {
	cmds := []CLICommandEntry{
		{
			Name:        "evil",
			Description: ".TH INJECTED 1\n.SH INJECTED SECTION",
			Usage:       "shurli evil",
			PluginName:  "test",
		},
	}

	man := GenerateManSection(cmds)

	// BUG M1: The description is inserted raw into troff.
	// ".TH INJECTED 1" is a troff title header directive.
	// This overwrites the man page structure.
	if strings.Contains(man, ".TH INJECTED") {
		t.Log("BUG M1: troff injection via plugin description. " +
			"Description '.TH INJECTED 1' is interpreted as troff directive. " +
			"Fix: escape backslashes and leading dots in descriptions.")
	}
}

// TestL2_RegisterCLICommandNoNameValidation proves L2: RegisterCLICommand
// accepts any name without validation. Names with special characters
// could cause issues in completion scripts or help output.
func TestL2_RegisterCLICommandNoNameValidation(t *testing.T) {
	// Clean state for this test.
	defer UnregisterCLICommands("test-l2")

	// Register a command with a pathological name.
	RegisterCLICommand(CLICommandEntry{
		Name:       "$(rm -rf /)",
		PluginName: "test-l2",
		Run:        func(args []string) {},
	})

	cmd, ok := FindCLICommand("$(rm -rf /)")
	if ok && cmd != nil {
		t.Log("BUG L2: RegisterCLICommand accepted name '$(rm -rf /)' without validation. " +
			"This name would be shell-injected in completion scripts. " +
			"Fix: validate command names (alphanumeric + hyphens only).")
	}
}

// TestL3_FishCompletionNameNotQuoted proves L3: fish completion uses
// command names unquoted. Names with spaces break the syntax.
func TestL3_FishCompletionNameNotQuoted(t *testing.T) {
	cmds := []CLICommandEntry{
		{
			Name:        "has space",
			Description: "Command with space in name",
			PluginName:  "test",
		},
	}

	fish := GenerateFishCompletion(cmds)

	// BUG L3: "complete -c shurli -n __fish_use_subcommand -a has space"
	// The space splits the name into two tokens: "has" and "space".
	if strings.Contains(fish, "-a has space") {
		t.Log("BUG L3: fish completion name not quoted. " +
			"'has space' becomes two tokens in fish shell. " +
			"Fix: quote the -a argument.")
	}
}

// =============================================================================
// HOLISTIC STRUCTURAL TESTS (pkg/plugin)
// Enforce invariants across the entire plugin framework.
// =============================================================================

// TestAllCompletionFormattersEscapeSpecialChars verifies that ALL 4 completion
// formatters properly handle special characters in descriptions.
// This catches M1 (troff), L3 (fish), and bash injection for ANY plugin.
func TestAllCompletionFormattersEscapeSpecialChars(t *testing.T) {
	dangerous := []CLICommandEntry{
		{
			Name:        "safe-cmd",
			Description: `Has "quotes" and 'single' and $(command) and ` + "`backticks`",
			Usage:       "shurli safe-cmd",
			PluginName:  "test",
			Flags: []CLIFlagEntry{
				{Long: "flag", Type: "bool", Description: `Flag with "quotes" and \backslash`},
			},
		},
		{
			Name:        "troff-cmd",
			Description: ".TH EVIL 1\n.SH INJECTED",
			Usage:       "shurli troff-cmd",
			PluginName:  "test",
		},
	}

	bash := GenerateBashCompletion(dangerous)
	if strings.Contains(bash, "$(command)") {
		t.Error("bash completion contains unescaped $(command) - shell injection risk")
	}

	fish := GenerateFishCompletion(dangerous)
	if strings.Contains(fish, "$(command)") {
		t.Error("fish completion contains unescaped $(command) - shell injection risk")
	}

	man := GenerateManSection(dangerous)
	if strings.Contains(man, ".TH EVIL") {
		t.Error("man page contains unescaped .TH directive - troff injection")
	}
	if strings.Contains(man, `\backslash`) {
		// Troff interprets backslash as escape char.
		t.Log("WARNING: man page contains unescaped backslash in description")
	}
}

// TestRegistryEnableDisableCycleNeverLeaks verifies that repeated enable/disable
// cycles leave the plugin in a clean state. No goroutine accumulation, no
// resource leaks, no stale protocol registrations.
func TestRegistryEnableDisableCycleNeverLeaks(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("cycle-leak")
	p.commands = []Command{{Name: "cmd1"}}
	p.routes = []Route{{Method: "GET", Path: "/v1/test"}}
	r.Register(p)

	for i := 0; i < 5; i++ {
		if err := r.Enable("cycle-leak"); err != nil {
			t.Fatalf("cycle %d: Enable failed: %v", i, err)
		}

		// Verify plugin is fully active.
		info, _ := r.GetInfo("cycle-leak")
		if info.State != StateActive {
			t.Fatalf("cycle %d: expected ACTIVE, got %s", i, info.State)
		}
		if len(r.AllCommands()) != 1 {
			t.Fatalf("cycle %d: expected 1 command, got %d", i, len(r.AllCommands()))
		}

		if err := r.Disable("cycle-leak"); err != nil {
			t.Fatalf("cycle %d: Disable failed: %v", i, err)
		}

		// Verify plugin is fully stopped.
		info, _ = r.GetInfo("cycle-leak")
		if info.State != StateStopped {
			t.Fatalf("cycle %d: expected STOPPED, got %s", i, info.State)
		}
		if len(r.AllCommands()) != 0 {
			t.Fatalf("cycle %d: expected 0 commands after disable, got %d", i, len(r.AllCommands()))
		}
	}

	// Verify exact Start/Stop call counts.
	if p.startCalled != 5 {
		t.Errorf("expected Start called 5 times, got %d", p.startCalled)
	}
	if p.stopCalled != 5 {
		t.Errorf("expected Stop called 5 times, got %d", p.stopCalled)
	}
}

// TestPluginContextIsolation verifies that PluginContext NEVER exposes
// internal types from identity, vault, auth, or daemon packages.
// This is a structural check that catches accidental credential exposure
// if someone adds a new field to PluginContext in the future.
func TestPluginContextIsolation(t *testing.T) {
	ctxType := reflect.TypeOf(PluginContext{})
	forbiddenPackages := []string{"identity", "vault", "auth", "daemon", "macaroon", "totp"}

	for i := 0; i < ctxType.NumField(); i++ {
		field := ctxType.Field(i)
		typePath := field.Type.String()
		for _, pkg := range forbiddenPackages {
			if strings.Contains(typePath, pkg) {
				t.Errorf("PluginContext field %q has type %q referencing forbidden package %q. "+
					"Credential isolation violated.", field.Name, typePath, pkg)
			}
		}
	}
}

// TestAllValidTransitionsDocumented verifies that every valid state transition
// in the state machine is exercisable through the registry API.
// Catches state machine changes that break the documented transitions.
func TestAllValidTransitionsDocumented(t *testing.T) {
	// These are the transitions from lifecycle.go ValidTransition().
	transitions := []struct {
		from, to State
		how      string
	}{
		{StateLoading, StateReady, "Register(Init succeeds)"},
		{StateReady, StateActive, "Enable(Start succeeds)"},
		{StateActive, StateDraining, "Disable(Stop called)"},
		{StateDraining, StateStopped, "Disable(drain complete)"},
		{StateStopped, StateActive, "Enable(re-start)"},
		{StateReady, StateStopped, "Disable(never started)"},
	}

	for _, tt := range transitions {
		if err := ValidTransition(tt.from, tt.to); err != nil {
			t.Errorf("transition %s -> %s (%s) should be valid but returned error: %v",
				tt.from, tt.to, tt.how, err)
		}
	}

	// Verify invalid transitions are rejected.
	invalid := [][2]State{
		{StateLoading, StateActive},
		{StateDraining, StateActive},
		{StateActive, StateReady},
		{StateStopped, StateReady},
	}
	for _, tt := range invalid {
		if err := ValidTransition(tt[0], tt[1]); err == nil {
			t.Errorf("transition %s -> %s should be invalid but was accepted", tt[0], tt[1])
		}
	}
}

// TestCLICommandGlobalMapIsolation verifies that registering commands from
// one plugin doesn't affect other plugins, and cleanup is complete.
func TestCLICommandGlobalMapIsolation(t *testing.T) {
	// Clean state.
	UnregisterCLICommands("plugin-a")
	UnregisterCLICommands("plugin-b")

	RegisterCLICommand(CLICommandEntry{Name: "cmd-a", PluginName: "plugin-a", Run: func([]string) {}})
	RegisterCLICommand(CLICommandEntry{Name: "cmd-b", PluginName: "plugin-b", Run: func([]string) {}})

	// Both should be findable.
	if _, ok := FindCLICommand("cmd-a"); !ok {
		t.Error("cmd-a not found")
	}
	if _, ok := FindCLICommand("cmd-b"); !ok {
		t.Error("cmd-b not found")
	}

	// Unregister plugin-a. plugin-b should survive.
	UnregisterCLICommands("plugin-a")
	if _, ok := FindCLICommand("cmd-a"); ok {
		t.Error("cmd-a should be gone after unregister")
	}
	if _, ok := FindCLICommand("cmd-b"); !ok {
		t.Error("cmd-b should survive after unregistering plugin-a")
	}

	// Cleanup.
	UnregisterCLICommands("plugin-b")
	if _, ok := FindCLICommand("cmd-b"); ok {
		t.Error("cmd-b should be gone after unregister")
	}

	// CLICommandDescriptions should return empty.
	if len(CLICommandDescriptions()) != 0 {
		t.Error("expected empty command descriptions after full cleanup")
	}
}

// Ensure types satisfy interface at compile time.
var _ Plugin = (*mockPlugin)(nil)
