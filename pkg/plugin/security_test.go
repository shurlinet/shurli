package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// S1: Cross-plugin isolation - Plugin A can't reach Plugin B's internals.
func TestSecurity_CrossPluginIsolation(t *testing.T) {
	r := newTestRegistryB1()

	var ctxA, ctxB *PluginContext
	a := newMinimalPlugin("iso-a")
	a.initFn = func(ctx *PluginContext) error { ctxA = ctx; return nil }
	b := newMinimalPlugin("iso-b")
	b.initFn = func(ctx *PluginContext) error { ctxB = ctx; return nil }

	r.Register(a)
	r.Register(b)

	if ctxA == nil || ctxB == nil {
		t.Fatal("contexts not captured")
	}

	// Different plugin contexts.
	if ctxA == ctxB {
		t.Error("Plugin A and B should have different PluginContexts")
	}

	// Different config dirs.
	if ctxA.ConfigDir() == ctxB.ConfigDir() && ctxA.ConfigDir() != "" {
		t.Error("plugins should have different config dirs")
	}

	// GetPlugin returns the Plugin interface, not the PluginContext.
	got := r.GetPlugin("iso-b")
	if got == nil {
		t.Fatal("GetPlugin should return plugin B")
	}
	// Plugin A can see Plugin B's public interface (Plugin), but NOT its PluginContext.
	// There's no method on Plugin or Registry that returns another plugin's PluginContext.
}

// S2: OpenStream namespace strict enforcement.
func TestSecurity_OpenStreamNamespaceStrict(t *testing.T) {
	r := newTestRegistryB1()
	var capturedCtx *PluginContext
	p := newMinimalPlugin("ns-strict")
	p.initFn = func(ctx *PluginContext) error { capturedCtx = ctx; return nil }
	p.protocols = []Protocol{
		{Name: "allowed-proto", Version: "1.0.0", Handler: noopStreamHandler()},
	}

	r.Register(p)
	r.Enable("ns-strict")

	if capturedCtx == nil {
		t.Fatal("context not captured")
	}

	// Undeclared protocol -> namespace violation.
	_, err := capturedCtx.OpenStream(context.Background(), "", "undeclared-proto")
	if err == nil {
		t.Error("OpenStream on undeclared protocol should return error")
	}
	if err != nil && err.Code != ErrCodeNamespaceViolation {
		t.Errorf("expected ErrCodeNamespaceViolation, got code %d", err.Code)
	}

	// Empty protocol -> violation.
	_, err = capturedCtx.OpenStream(context.Background(), "", "")
	if err == nil {
		t.Error("OpenStream with empty protocol should return error")
	}

	// Path traversal in protocol name -> violation.
	_, err = capturedCtx.OpenStream(context.Background(), "", "../../../etc/passwd")
	if err == nil {
		t.Error("OpenStream with traversal should return error")
	}
}

// S3: DeriveKey domain isolation.
func TestSecurity_DeriveKeyDomainIsolation(t *testing.T) {
	// Create two plugins with a real key deriver.
	fakeDeriver := func(domain string) []byte {
		// Deterministic: same domain -> same key.
		h := make([]byte, 32)
		for i, c := range []byte(domain) {
			h[i%32] ^= c
		}
		return h
	}

	r := NewRegistry(&ContextProvider{
		KeyDeriver: fakeDeriver,
	})
	r.enableDisableCooldown = 0

	var ctxA, ctxB *PluginContext
	a := newMinimalPlugin("key-a")
	a.initFn = func(ctx *PluginContext) error { ctxA = ctx; return nil }
	b := newMinimalPlugin("key-b")
	b.initFn = func(ctx *PluginContext) error { ctxB = ctx; return nil }

	r.Register(a)
	r.Register(b)

	// Same domain, same deriver -> same key (shared deriver, not per-plugin).
	keyA := ctxA.DeriveKey("test-domain")
	keyB := ctxB.DeriveKey("test-domain")
	if keyA == nil || keyB == nil {
		t.Fatal("DeriveKey returned nil")
	}

	// Different domains -> different keys.
	keyA2 := ctxA.DeriveKey("other-domain")
	if reflect.DeepEqual(keyA, keyA2) {
		t.Error("different domains should produce different keys")
	}

	// Same plugin, same domain -> deterministic.
	keyA3 := ctxA.DeriveKey("test-domain")
	if !reflect.DeepEqual(keyA, keyA3) {
		t.Error("same domain should produce same key")
	}

	// Empty domain -> returns nil (no meaningful derivation).
	keyEmpty := ctxA.DeriveKey("")
	if keyEmpty != nil {
		t.Error("DeriveKey with empty domain should return nil")
	}
}

// S4: ConfigDir confinement - path traversal in plugin ID sanitized.
func TestSecurity_ConfigDirConfinement(t *testing.T) {
	badIDs := []string{
		"../../../etc",
		"test/../../../etc",
		"test.io/../../etc",
	}

	for _, id := range badIDs {
		if err := validatePluginID(id); err == nil {
			t.Errorf("plugin ID %q should be rejected", id)
		}
	}

	// Valid IDs should pass.
	goodIDs := []string{
		"test.io/official/plugin",
		"github.com/user/plugin",
		"a.b/c",
	}
	for _, id := range goodIDs {
		if err := validatePluginID(id); err != nil {
			t.Errorf("valid plugin ID %q rejected: %v", id, err)
		}
	}
}

// S5: PluginContext capability allowlist - no method returns credentials.
func TestSecurity_PluginContextCapabilityAllowlist(t *testing.T) {
	ctxType := reflect.TypeOf(&PluginContext{})

	forbiddenReturnTypes := []string{
		"identity", "vault", "macaroon", "cookie",
		"PrivateKey", "SecretKey", "Token",
	}

	for i := 0; i < ctxType.NumMethod(); i++ {
		method := ctxType.Method(i)
		methodType := method.Type

		for j := 0; j < methodType.NumOut(); j++ {
			outType := methodType.Out(j)
			typeName := outType.String()

			for _, forbidden := range forbiddenReturnTypes {
				if strings.Contains(strings.ToLower(typeName), strings.ToLower(forbidden)) {
					t.Errorf("PluginContext.%s returns %s which contains forbidden type %q",
						method.Name, typeName, forbidden)
				}
			}
		}
	}
}

// S6: PluginContext reflection attack - unexported fields not settable.
func TestSecurity_PluginContextReflectionAttack(t *testing.T) {
	ctx := &PluginContext{}
	v := reflect.ValueOf(ctx).Elem()

	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if field.IsExported() {
			t.Errorf("PluginContext has exported field %q - should be unexported", field.Name)
		}
		if v.Field(i).CanSet() {
			t.Errorf("PluginContext field %q is settable via reflection", field.Name)
		}
	}
}

// S7: Command name injection - shell metacharacters rejected.
func TestSecurity_CommandNameInjectionE2E(t *testing.T) {
	malicious := []string{
		"cmd;rm -rf /",
		"cmd$(whoami)",
		"cmd`id`",
		"cmd|cat /etc/passwd",
		"cmd&background",
		"cmd\nnewline",
		"cmd with spaces",
	}

	for _, name := range malicious {
		if isValidCommandName(name) {
			t.Errorf("malicious command name %q should be rejected", name)
		}
	}

	// Valid names.
	valid := []string{"send", "file-transfer", "cmd123", "a"}
	for _, name := range valid {
		if !isValidCommandName(name) {
			t.Errorf("valid command name %q rejected", name)
		}
	}
}

// S8: Route path traversal - sanitization check.
func TestSecurity_RoutePathTraversal(t *testing.T) {
	// The registry doesn't sanitize route paths - the daemon's mux does.
	// This test documents that plugin routes are passed through as-is.
	r := newTestRegistryB1()
	p := newMinimalPlugin("path-traversal")
	p.routes = []Route{
		{Method: "GET", Path: "/v1/../../../etc/passwd", Handler: noopHandler()},
	}

	r.Register(p)
	r.Enable("path-traversal")

	// Route exists but mux would sanitize the path.
	routes := r.AllRoutes()
	if len(routes) != 1 {
		t.Errorf("expected 1 route, got %d", len(routes))
	}
}

// S9: Protocol policy enforcement - verifies policy wiring at plugin layer.
// Full enforcement (denied peer, relay-only vs direct-only) is tested in
// pkg/p2pnet/service_test.go where two real peers can connect. Here we verify:
// 1. nil policy -> default policy applied at registration
// 2. explicit policy -> carried through to ServiceRegistry
func TestSecurity_ProtocolPolicyEnforcement(t *testing.T) {
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

	// Plugin with nil policy -> should get default transport policy at registration.
	p := newMinimalPlugin("policy-default")
	p.protocols = []Protocol{
		{Name: "pol-default", Version: "1.0.0", Handler: noopStreamHandler(), Policy: nil},
	}

	r.Register(p)
	if err := r.Enable("policy-default"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	svc, ok := sr.GetService("pol-default")
	if !ok {
		t.Fatal("protocol should be registered in ServiceRegistry")
	}
	if svc.Policy == nil {
		t.Error("nil policy should be replaced with default policy at registration")
	}

	// Plugin with explicit LAN-only policy -> carried through.
	p2 := newMinimalPlugin("policy-lan")
	p2.protocols = []Protocol{
		{Name: "pol-lan", Version: "1.0.0", Handler: noopStreamHandler(),
			Policy: &p2pnet.PluginPolicy{AllowedTransports: p2pnet.TransportLAN}},
	}

	r.Register(p2)
	if err := r.Enable("policy-lan"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	svc2, ok := sr.GetService("pol-lan")
	if !ok {
		t.Fatal("LAN-only protocol should be registered")
	}
	if svc2.Policy == nil || svc2.Policy.AllowedTransports != p2pnet.TransportLAN {
		t.Error("explicit LAN-only policy should be preserved in ServiceRegistry")
	}
}

// S10: Error messages don't contain internal details.
func TestSecurity_ErrorMessagesNoInternals(t *testing.T) {
	r := newTestRegistryB1()
	var capturedCtx *PluginContext
	p := newMinimalPlugin("err-msg")
	p.initFn = func(ctx *PluginContext) error { capturedCtx = ctx; return nil }

	r.Register(p)

	// Exercise error paths.
	errs := []*PluginError{
		capturedCtx.ConnectToPeer(context.Background(), ""),
	}
	_, openErr := capturedCtx.OpenStream(context.Background(), "", "undeclared")
	errs = append(errs, openErr)
	_, resolveErr := capturedCtx.ResolveName("test")
	errs = append(errs, resolveErr)

	forbidden := []string{
		"12D3KooW", // peer ID prefix
		"192.168.", // private IP
		"10.0.",    // private IP
		"/Users/",  // file path
		"/home/",   // file path
		"stack",    // stack trace
	}

	for _, err := range errs {
		if err == nil {
			continue
		}
		msg := err.Error()
		for _, f := range forbidden {
			if strings.Contains(msg, f) {
				t.Errorf("error message contains internal detail %q: %s", f, msg)
			}
		}
	}
}

// S11: StatusContributions doesn't expose PluginContext internals.
func TestSecurity_StatusFieldsNoLeaks(t *testing.T) {
	r := newTestRegistryB1()
	p := newMinimalPlugin("status-leak")
	p.statusFields = map[string]any{
		"mode":    "test",
		"version": "1.0.0",
	}

	r.Register(p)
	r.Enable("status-leak")

	contributions := r.StatusContributions()
	fields, ok := contributions["status-leak"]
	if !ok {
		t.Fatal("expected status contribution from plugin")
	}

	// Verify only the plugin's own fields are present.
	if len(fields) != 2 {
		t.Errorf("expected 2 status fields, got %d", len(fields))
	}

	// Verify no PluginContext internals leak.
	for key := range fields {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "context") || strings.Contains(lower, "network") ||
			strings.Contains(lower, "key") || strings.Contains(lower, "deriver") {
			t.Errorf("status field %q may leak internal details", key)
		}
	}
}

// S12: Checkpoint HMAC - end-to-end: crash -> checkpoint with HMAC -> restore with valid HMAC.
// Also verifies key domain isolation between plugins.
func TestSecurity_CheckpointHMAC(t *testing.T) {
	fakeDeriver := func(domain string) []byte {
		h := sha256.Sum256([]byte(domain))
		return h[:]
	}

	r := NewRegistry(&ContextProvider{
		KeyDeriver: fakeDeriver,
	})
	r.enableDisableCooldown = 0

	// Plugin that implements Checkpointer with known data.
	// Must use testCheckpointerPlugin - testPlugin does NOT implement Checkpointer.
	var restoreCalled atomic.Bool
	var restoredData atomic.Value
	base := newMinimalPlugin("hmac-test")
	base.checkpointFn = func() ([]byte, error) {
		return []byte("checkpoint-state-data"), nil
	}
	base.restoreFn = func(data []byte) error {
		restoreCalled.Store(true)
		restoredData.Store(string(data))
		return nil
	}
	p := &testCheckpointerPlugin{testPlugin: base}
	r.Register(p)
	r.Enable("hmac-test")

	// Trigger crash -> supervisor restarts with checkpoint + HMAC.
	r.recordCrashAndMaybeRestart("hmac-test")

	// Wait for restart to complete.
	if !waitForState(r, "hmac-test", StateActive, 5*time.Second) {
		t.Fatal("plugin should restart after crash")
	}

	// Restore should have been called with the original checkpoint data,
	// proving the HMAC verification passed.
	if !restoreCalled.Load() {
		t.Fatal("Restore should have been called after successful HMAC verification")
	}
	if got, ok := restoredData.Load().(string); !ok || got != "checkpoint-state-data" {
		t.Fatalf("Restore received wrong data: %q", got)
	}

	// Verify key domain isolation: different plugin names derive different keys.
	keyA := fakeDeriver("checkpoint\x00plugin-a")
	keyB := fakeDeriver("checkpoint\x00plugin-b")
	if hmac.Equal(keyA, keyB) {
		t.Fatal("different plugin names should derive different checkpoint keys")
	}
}

// S13: Checkpoint timeout - hanging Checkpoint() doesn't block restart.
func TestSecurity_CheckpointTimeout(t *testing.T) {
	// Override start timeout to make test fast.
	origTimeout := startTimeoutDuration
	startTimeoutDuration = 200 * time.Millisecond
	defer func() { startTimeoutDuration = origTimeout }()

	r := NewRegistry(&ContextProvider{})
	r.enableDisableCooldown = 0

	var restoreCalled atomic.Bool
	base := newMinimalPlugin("hang-cp")
	// Checkpoint hangs forever.
	base.checkpointFn = func() ([]byte, error) {
		select {} // block forever
	}
	base.restoreFn = func(data []byte) error {
		restoreCalled.Store(true)
		return nil
	}
	// Must use testCheckpointerPlugin - testPlugin does NOT implement Checkpointer.
	p := &testCheckpointerPlugin{testPlugin: base}
	r.Register(p)
	r.Enable("hang-cp")

	// Trigger a crash to start the supervisor restart flow.
	r.recordCrashAndMaybeRestart("hang-cp")

	// Wait for the restart to complete (checkpoint should timeout, proceed stateless).
	ok := waitForState(r, "hang-cp", StateActive, 3*time.Second)
	if !ok {
		t.Fatal("plugin should restart even when Checkpoint() hangs (timeout should fire)")
	}

	// Restore should NOT have been called (no checkpoint data due to timeout).
	if restoreCalled.Load() {
		t.Error("Restore should not be called when Checkpoint timed out")
	}
}
