package plugin

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// testPlugin is a fully injectable Plugin implementation for tests.
// Every lifecycle method delegates to a configurable function. Call counters
// are atomic for -race safety. All test files in pkg/plugin use this.
type testPlugin struct {
	id, name, version string

	// Injectable lifecycle functions. nil = no-op success.
	initFn           func(*PluginContext) error
	startFn          func(context.Context) error
	stopFn           func() error
	onNetworkReadyFn func() error

	// Checkpointer support (optional). Both nil = not a Checkpointer.
	checkpointFn func() ([]byte, error)
	restoreFn    func([]byte) error

	// Declarations.
	commands      []Command
	routes        []Route
	protocols     []Protocol
	configSection string
	statusFields  map[string]any

	// Tracking (atomic for -race safety).
	initCalls         atomic.Int32
	startCalls        atomic.Int32
	stopCalls         atomic.Int32
	networkReadyCalls atomic.Int32

	// Last context received (use atomic for -race safety in concurrent tests).
	lastInitCtx  atomic.Value // stores *PluginContext
	lastStartCtx atomic.Value // stores context.Context
}

func (p *testPlugin) ID() string            { return p.id }
func (p *testPlugin) Name() string          { return p.name }
func (p *testPlugin) Version() string       { return p.version }
func (p *testPlugin) ConfigSection() string { return p.configSection }
func (p *testPlugin) Commands() []Command   { return p.commands }
func (p *testPlugin) Routes() []Route       { return p.routes }
func (p *testPlugin) Protocols() []Protocol { return p.protocols }

func (p *testPlugin) Init(ctx *PluginContext) error {
	p.initCalls.Add(1)
	p.lastInitCtx.Store(ctx)
	if p.initFn != nil {
		return p.initFn(ctx)
	}
	return nil
}

func (p *testPlugin) Start(ctx context.Context) error {
	p.startCalls.Add(1)
	p.lastStartCtx.Store(&ctxWrapper{ctx})
	if p.startFn != nil {
		return p.startFn(ctx)
	}
	return nil
}

// ctxWrapper wraps context.Context so it can be stored in atomic.Value
// (atomic.Value requires consistent concrete types).
type ctxWrapper struct{ context.Context }

func (p *testPlugin) Stop() error {
	p.stopCalls.Add(1)
	if p.stopFn != nil {
		return p.stopFn()
	}
	return nil
}

func (p *testPlugin) OnNetworkReady() error {
	p.networkReadyCalls.Add(1)
	if p.onNetworkReadyFn != nil {
		return p.onNetworkReadyFn()
	}
	return nil
}

// StatusFields implements StatusContributor if statusFields is set.
func (p *testPlugin) StatusFields() map[string]any {
	return p.statusFields
}

// testCheckpointerPlugin wraps testPlugin and explicitly implements Checkpointer.
// testPlugin itself does NOT implement Checkpointer - this separation lets tests
// verify that the registry correctly detects (or doesn't detect) the interface.
type testCheckpointerPlugin struct {
	*testPlugin
}

func (p *testCheckpointerPlugin) Checkpoint() ([]byte, error) {
	if p.checkpointFn != nil {
		return p.checkpointFn()
	}
	return nil, ErrSkipCheckpoint
}

func (p *testCheckpointerPlugin) Restore(data []byte) error {
	if p.restoreFn != nil {
		return p.restoreFn(data)
	}
	return nil
}

var _ Checkpointer = (*testCheckpointerPlugin)(nil)

// --- Factory helpers ---

// newMinimalPlugin creates a no-op plugin with the given name.
func newMinimalPlugin(name string) *testPlugin {
	return &testPlugin{
		id:      "test.io/mock/" + name,
		name:    name,
		version: "1.0.0",
	}
}

// newCommandPlugin creates a plugin that provides the given commands.
func newCommandPlugin(name string, cmds []Command) *testPlugin {
	p := newMinimalPlugin(name)
	p.commands = cmds
	return p
}

// newRoutePlugin creates a plugin that provides the given routes.
func newRoutePlugin(name string, routes []Route) *testPlugin {
	p := newMinimalPlugin(name)
	p.routes = routes
	return p
}

// newProtocolPlugin creates a plugin that provides the given protocols.
func newProtocolPlugin(name string, protos []Protocol) *testPlugin {
	p := newMinimalPlugin(name)
	p.protocols = protos
	return p
}

// newHangingPlugin creates a plugin that blocks forever on the specified method.
func newHangingPlugin(name string, hangOn string) *testPlugin {
	p := newMinimalPlugin(name)
	block := func() error {
		select {} // block forever
	}
	switch hangOn {
	case "init":
		p.initFn = func(_ *PluginContext) error { return block() }
	case "start":
		p.startFn = func(_ context.Context) error { return block() }
	case "stop":
		p.stopFn = func() error { return block() }
	case "network-ready":
		p.onNetworkReadyFn = func() error { return block() }
	}
	return p
}

// newPanickingPlugin creates a plugin that panics on the specified method.
func newPanickingPlugin(name string, panicOn string) *testPlugin {
	p := newMinimalPlugin(name)
	doPanic := func() { panic("test panic in " + panicOn) }
	switch panicOn {
	case "init":
		p.initFn = func(_ *PluginContext) error { doPanic(); return nil }
	case "start":
		p.startFn = func(_ context.Context) error { doPanic(); return nil }
	case "stop":
		p.stopFn = func() error { doPanic(); return nil }
	case "network-ready":
		p.onNetworkReadyFn = func() error { doPanic(); return nil }
	case "checkpoint":
		p.checkpointFn = func() ([]byte, error) { doPanic(); return nil, nil }
	case "restore":
		p.restoreFn = func([]byte) error { doPanic(); return nil }
	}
	return p
}

// newErrorPlugin creates a plugin that returns an error on the specified method.
func newErrorPlugin(name string, errOn string, err error) *testPlugin {
	p := newMinimalPlugin(name)
	switch errOn {
	case "init":
		p.initFn = func(_ *PluginContext) error { return err }
	case "start":
		p.startFn = func(_ context.Context) error { return err }
	case "stop":
		p.stopFn = func() error { return err }
	case "network-ready":
		p.onNetworkReadyFn = func() error { return err }
	}
	return p
}

// newSlowPlugin creates a plugin that delays on the specified method.
func newSlowPlugin(name string, delay time.Duration, slowOn string) *testPlugin {
	p := newMinimalPlugin(name)
	doSlow := func() error {
		time.Sleep(delay)
		return nil
	}
	switch slowOn {
	case "start":
		p.startFn = func(_ context.Context) error { return doSlow() }
	case "stop":
		p.stopFn = func() error { return doSlow() }
	case "network-ready":
		p.onNetworkReadyFn = func() error { return doSlow() }
	}
	return p
}

// newCheckpointPlugin creates a plugin that implements Checkpointer with
// configurable state. Returns a testCheckpointerPlugin (satisfies Checkpointer).
func newCheckpointPlugin(name string, data []byte) *testCheckpointerPlugin {
	p := newMinimalPlugin(name)
	var savedData []byte
	if data != nil {
		savedData = make([]byte, len(data))
		copy(savedData, data)
	}
	p.checkpointFn = func() ([]byte, error) {
		if len(savedData) == 0 {
			return nil, ErrSkipCheckpoint
		}
		return savedData, nil
	}
	p.restoreFn = func(d []byte) error {
		savedData = make([]byte, len(d))
		copy(savedData, d)
		return nil
	}
	return &testCheckpointerPlugin{testPlugin: p}
}

// --- Test helpers ---

// noopHandler returns an HTTP handler that responds 200 OK.
func noopHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

// noopStreamHandler returns a no-op stream handler.
func noopStreamHandler() func(string, network.Stream) {
	return func(_ string, s network.Stream) {
		s.Close()
	}
}

// newTestRegistryB1 creates a Registry with test ContextProvider.
// Disables cooldown for fast tests. Named differently from the existing
// newTestRegistry to avoid collision during migration.
func newTestRegistryB1() *Registry {
	enableDisableCooldown = 0
	return NewRegistry(&ContextProvider{})
}

// waitForState polls GetInfo until the plugin reaches the expected state AND
// any in-flight supervisor restart goroutine has completed. Returns true if
// both conditions are met within the timeout, false if timed out.
// Use instead of time.Sleep for async operations (supervisor restart, etc.).
func waitForState(r *Registry, name string, want State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.RLock()
		entry, ok := r.plugins[name]
		if ok && entry.state == want && !entry.supervisor.restarting.Load() {
			r.mu.RUnlock()
			return true
		}
		r.mu.RUnlock()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
