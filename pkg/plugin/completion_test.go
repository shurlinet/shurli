package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- X5: Completion formatter tests ---

func testCommands() []CLICommandEntry {
	return []CLICommandEntry{
		{
			Name:        "send",
			Description: "Send a file to a peer",
			Flags: []CLIFlagEntry{
				{Long: "follow", Short: "f", Description: "Follow transfer progress", Type: "bool"},
				{Long: "json", Description: "Output as JSON", Type: "bool"},
				{Long: "priority", Description: "Transfer priority", Type: "enum", Enum: []string{"low", "normal", "high"}, RequiresArg: true},
			},
		},
		{
			Name:        "share",
			Description: "Manage shared files",
			Subcommands: []CLISubcommand{
				{Name: "add", Description: "Add a share", Flags: []CLIFlagEntry{
					{Long: "persistent", Description: "Persist across restarts", Type: "bool"},
				}},
				{Name: "remove", Description: "Remove a share"},
				{Name: "list", Description: "List shares"},
			},
		},
	}
}

func TestGenerateBashCompletion(t *testing.T) {
	cmds := testCommands()
	result := GenerateBashCompletion(cmds)

	if result == "" {
		t.Fatal("expected non-empty bash completion output")
	}
	// Should contain plugin command names.
	if !strings.Contains(result, "send") {
		t.Error("missing 'send' in bash completion")
	}
	if !strings.Contains(result, "share") {
		t.Error("missing 'share' in bash completion")
	}
	// Should contain flag names.
	if !strings.Contains(result, "--follow") {
		t.Error("missing '--follow' flag in bash completion")
	}
	// Should contain subcommand names.
	if !strings.Contains(result, "add") {
		t.Error("missing 'add' subcommand in bash completion")
	}
}

func TestGenerateBashCompletionEmpty(t *testing.T) {
	result := GenerateBashCompletion(nil)
	if result != "" {
		t.Errorf("expected empty output for nil commands, got %q", result)
	}
}

func TestGenerateZshCompletion(t *testing.T) {
	cmds := testCommands()
	result := GenerateZshCompletion(cmds)

	if result == "" {
		t.Fatal("expected non-empty zsh completion output")
	}
	if !strings.Contains(result, "send") {
		t.Error("missing 'send' in zsh completion")
	}
	if !strings.Contains(result, "_describe") || !strings.Contains(result, "_arguments") {
		t.Error("missing zsh completion directives")
	}
	// Should contain enum values for priority.
	if !strings.Contains(result, "low normal high") {
		t.Error("missing enum values in zsh completion")
	}
}

func TestGenerateZshCompletionEmpty(t *testing.T) {
	result := GenerateZshCompletion(nil)
	if result != "" {
		t.Errorf("expected empty output for nil commands, got %q", result)
	}
}

func TestGenerateFishCompletion(t *testing.T) {
	cmds := testCommands()
	result := GenerateFishCompletion(cmds)

	if result == "" {
		t.Fatal("expected non-empty fish completion output")
	}
	if !strings.Contains(result, "complete -c shurli") {
		t.Error("missing fish 'complete -c shurli' directive")
	}
	if !strings.Contains(result, "__fish_use_subcommand") {
		t.Error("missing __fish_use_subcommand in fish completion")
	}
	if !strings.Contains(result, "__fish_seen_subcommand_from") {
		t.Error("missing __fish_seen_subcommand_from in fish completion")
	}
	// L3 fix: names should be quoted.
	if !strings.Contains(result, "-a '") {
		t.Error("missing quoted -a argument in fish completion (L3 fix)")
	}
}

func TestGenerateFishCompletionEmpty(t *testing.T) {
	result := GenerateFishCompletion(nil)
	if result != "" {
		t.Errorf("expected empty output for nil commands, got %q", result)
	}
}

func TestGenerateManSection(t *testing.T) {
	cmds := testCommands()
	result := GenerateManSection(cmds)

	if result == "" {
		t.Fatal("expected non-empty man section output")
	}
	if !strings.Contains(result, ".TP") {
		t.Error("missing .TP troff directive in man section")
	}
	if !strings.Contains(result, "\\fBsend\\fR") {
		t.Error("missing bold 'send' in man section")
	}
	if !strings.Contains(result, "\\fB--follow\\fR") {
		t.Error("missing bold '--follow' in man section")
	}
}

func TestGenerateManSectionEmpty(t *testing.T) {
	result := GenerateManSection(nil)
	if result != "" {
		t.Errorf("expected empty output for nil commands, got %q", result)
	}
}

// --- Escape function tests ---

func TestEscapeTroff(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{`hello\world`, func(s string) bool { return strings.Contains(s, `\\`) }, "backslash doubled"},
		{".TH INJECT", func(s string) bool { return !strings.HasPrefix(s, ".") }, "leading dot stripped"},
		{"line1\nline2", func(s string) bool { return !strings.Contains(s, "\n") }, "newlines removed"},
		{"normal text", func(s string) bool { return s == "normal text" }, "clean text unchanged"},
	}
	for _, tt := range tests {
		result := escapeTroff(tt.input)
		if !tt.check(result) {
			t.Errorf("escapeTroff(%q) = %q: %s", tt.input, result, tt.desc)
		}
	}
}

func TestEscapeZshDesc(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{"has [brackets]", func(s string) bool { return !strings.Contains(s, "[") }, "brackets replaced"},
		{"has:colon", func(s string) bool { return !strings.Contains(s, ":") }, "colon replaced"},
		{`has\backslash`, func(s string) bool { return strings.Contains(s, `\\`) }, "backslash escaped"},
	}
	for _, tt := range tests {
		result := escapeZshDesc(tt.input)
		if !tt.check(result) {
			t.Errorf("escapeZshDesc(%q) = %q: %s", tt.input, result, tt.desc)
		}
	}
}

func TestEscapeFish(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{"has'quote", func(s string) bool { return strings.Contains(s, "\\'") }, "quote escaped"},
		{"has$var", func(s string) bool { return strings.Contains(s, `\$`) }, "dollar escaped"},
		{"has`cmd`", func(s string) bool { return strings.Contains(s, "\\`") }, "backtick escaped"},
	}
	for _, tt := range tests {
		result := escapeFish(tt.input)
		if !tt.check(result) {
			t.Errorf("escapeFish(%q) = %q: %s", tt.input, result, tt.desc)
		}
	}
}

func TestEscapeShellDesc(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{`has"quote`, func(s string) bool { return !strings.Contains(s, `"`) }, "double quote replaced"},
		{"has$(cmd)", func(s string) bool { return !strings.Contains(s, "$(") }, "command substitution stripped"},
		{"has${var}", func(s string) bool { return !strings.Contains(s, "${") }, "variable expansion stripped"},
	}
	for _, tt := range tests {
		result := escapeShellDesc(tt.input)
		if !tt.check(result) {
			t.Errorf("escapeShellDesc(%q) = %q: %s", tt.input, result, tt.desc)
		}
	}
}

// --- F47: Commands/RegisterCLI sync test ---

func TestF47_CLICommandsSyncWithCommands(t *testing.T) {
	// Verify that CLICommandDescriptions returns entries that match
	// what was registered via RegisterCLICommand.
	// Clean slate.
	cliMu.Lock()
	orig := make(map[string]*CLICommandEntry)
	for k, v := range cliCommands {
		orig[k] = v
	}
	cliCommands = make(map[string]*CLICommandEntry)
	cliMu.Unlock()
	defer func() {
		cliMu.Lock()
		cliCommands = orig
		cliMu.Unlock()
	}()

	// Register two commands.
	RegisterCLICommand(CLICommandEntry{Name: "alpha", Description: "Alpha cmd", PluginName: "test"})
	RegisterCLICommand(CLICommandEntry{Name: "beta", Description: "Beta cmd", PluginName: "test"})

	descs := CLICommandDescriptions()
	if len(descs) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(descs))
	}
	// Should be sorted.
	if descs[0].Name != "alpha" || descs[1].Name != "beta" {
		t.Errorf("commands not sorted: %s, %s", descs[0].Name, descs[1].Name)
	}

	// Unregister and verify empty.
	UnregisterCLICommands("test")
	descs = CLICommandDescriptions()
	if len(descs) != 0 {
		t.Errorf("expected 0 commands after unregister, got %d", len(descs))
	}
}

// --- L1: coreRouteKeys sync test ---
// This test is in the plugin package because it validates that plugin routes
// don't accidentally collide with core routes. The actual coreRouteKeys map
// is in internal/daemon/handlers.go and tested via the daemon package.
// Here we verify that plugin-declared routes use unique paths.

func TestL1_PluginRoutesNoCollisionWithCorePatterns(t *testing.T) {
	// Core route patterns that plugin routes must NEVER match.
	corePatterns := []string{
		"/v1/status", "/v1/services", "/v1/peers", "/v1/auth",
		"/v1/paths", "/v1/bandwidth", "/v1/relay-health",
		"/v1/ping", "/v1/traceroute", "/v1/resolve",
		"/v1/connect", "/v1/expose", "/v1/shutdown",
		"/v1/lock", "/v1/unlock", "/v1/invite",
		"/v1/config/reload", "/v1/plugins",
	}
	coreSet := make(map[string]bool)
	for _, p := range corePatterns {
		coreSet[p] = true
	}

	// Create a mock plugin with routes.
	mock := &mockPlugin{name: "route-test"}
	mock.routes = []Route{
		{Method: "GET", Path: "/v1/test-route"},
		{Method: "POST", Path: "/v1/custom"},
	}

	for _, r := range mock.routes {
		if coreSet[r.Path] {
			t.Errorf("plugin route %s %s collides with core route", r.Method, r.Path)
		}
	}
}

// --- L2: RegisterCLICommand name validation test ---

func TestL2_InvalidCommandNameRejected(t *testing.T) {
	cliMu.Lock()
	orig := make(map[string]*CLICommandEntry)
	for k, v := range cliCommands {
		orig[k] = v
	}
	cliCommands = make(map[string]*CLICommandEntry)
	cliMu.Unlock()
	defer func() {
		cliMu.Lock()
		cliCommands = orig
		cliMu.Unlock()
	}()

	// Valid name should register.
	RegisterCLICommand(CLICommandEntry{Name: "valid-cmd", PluginName: "test"})
	if _, ok := FindCLICommand("valid-cmd"); !ok {
		t.Error("valid command name should be registered")
	}

	// Invalid names should be silently rejected.
	invalid := []string{"has space", "has;semi", "../traversal", "", strings.Repeat("x", 65)}
	for _, name := range invalid {
		RegisterCLICommand(CLICommandEntry{Name: name, PluginName: "test"})
		if _, ok := FindCLICommand(name); ok {
			t.Errorf("invalid command name %q should NOT be registered", name)
		}
	}
}

// --- X3: GetPlugin test ---

func TestX3_GetPlugin(t *testing.T) {
	r := newTestRegistry()
	mock := &mockPlugin{name: "lookup-test"}
	r.Register(mock)

	p := r.GetPlugin("lookup-test")
	if p == nil {
		t.Fatal("GetPlugin should return registered plugin")
	}
	if p.Name() != "lookup-test" {
		t.Errorf("unexpected name: %s", p.Name())
	}

	// Non-existent plugin.
	if r.GetPlugin("nonexistent") != nil {
		t.Error("GetPlugin should return nil for unknown plugin")
	}
}

// --- G2: Reserved protocol name test ---

func TestG2_ReservedProtocolNameRejected(t *testing.T) {
	reserved := []string{"relay-pair", "relay-unseal", "relay-admin", "relay-motd", "peer-notify", "zkp-auth", "ping", "kad"}
	for _, name := range reserved {
		if !isReservedProtocolName(name) {
			t.Errorf("protocol name %q should be reserved", name)
		}
	}
	// Non-reserved names.
	nonReserved := []string{"file-transfer", "file-browse", "custom-proto", "my-plugin"}
	for _, name := range nonReserved {
		if isReservedProtocolName(name) {
			t.Errorf("protocol name %q should NOT be reserved", name)
		}
	}
}

// --- #10: Plugin dir permissions hard error test ---

func TestPluginDirPermissionsHardError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	os.MkdirAll(configDir, 0700)

	// Create plugin dir with wrong permissions.
	// mockPlugin ID = "test.io/mock/perms-test"
	pluginDir := filepath.Join(configDir, "plugins", "test.io", "mock", "perms-test")
	os.MkdirAll(pluginDir, 0755) // intentionally wrong

	mock := &mockPlugin{name: "perms-test"}
	r := NewRegistry(&ContextProvider{ConfigDir: configDir})

	err := r.Register(mock)
	if err == nil {
		t.Fatal("Register should fail when plugin dir has unsafe permissions")
	}
	if !strings.Contains(err.Error(), "unsafe permissions") {
		t.Errorf("error should mention unsafe permissions, got: %v", err)
	}
}
