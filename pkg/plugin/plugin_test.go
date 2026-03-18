package plugin

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

// Ensure types satisfy interface at compile time.
var _ Plugin = (*mockPlugin)(nil)
