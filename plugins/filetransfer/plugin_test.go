package filetransfer

import (
	"testing"

	"github.com/shurlinet/shurli/pkg/plugin"
)

func TestPluginInterface(t *testing.T) {
	p := New()
	if p.ID() != "shurli.io/official/filetransfer" {
		t.Errorf("unexpected ID: %s", p.ID())
	}
	if p.Name() != "filetransfer" {
		t.Errorf("unexpected Name: %s", p.Name())
	}
	if p.ConfigSection() != "filetransfer" {
		t.Errorf("unexpected ConfigSection: %s", p.ConfigSection())
	}
}

func TestPluginImplementsInterfaces(t *testing.T) {
	var _ plugin.Plugin = (*FileTransferPlugin)(nil)
	var _ plugin.StatusContributor = (*FileTransferPlugin)(nil)
}

func TestCommands(t *testing.T) {
	p := New()
	cmds := p.Commands()
	if len(cmds) != 9 {
		t.Errorf("expected 9 commands, got %d", len(cmds))
	}

	expected := map[string]bool{
		"send": false, "download": false, "browse": false,
		"share": false, "transfers": false, "accept": false,
		"reject": false, "cancel": false, "clean": false,
	}
	for _, cmd := range cmds {
		if _, ok := expected[cmd.Name]; !ok {
			t.Errorf("unexpected command: %s", cmd.Name)
		}
		expected[cmd.Name] = true
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing command: %s", name)
		}
	}
}

func TestRoutes(t *testing.T) {
	p := New()
	routes := p.Routes()
	if len(routes) != 14 {
		t.Errorf("expected 14 routes, got %d", len(routes))
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := loadConfig(nil)
	if cfg.ReceiveMode != "contacts" {
		t.Errorf("expected default receive_mode=contacts, got %s", cfg.ReceiveMode)
	}
}

func TestConfigParsing(t *testing.T) {
	yaml := []byte(`
receive_dir: /tmp/test
receive_mode: ask
max_file_size: 1073741824
compress: false
`)
	cfg := loadConfig(yaml)
	if cfg.ReceiveDir != "/tmp/test" {
		t.Errorf("unexpected receive_dir: %s", cfg.ReceiveDir)
	}
	if cfg.ReceiveMode != "ask" {
		t.Errorf("unexpected receive_mode: %s", cfg.ReceiveMode)
	}
	if cfg.MaxFileSize != 1073741824 {
		t.Errorf("unexpected max_file_size: %d", cfg.MaxFileSize)
	}
	if cfg.Compress == nil || *cfg.Compress != false {
		t.Error("expected compress=false")
	}
}

func TestCLICommandList(t *testing.T) {
	cmds := cliCommandList()
	if len(cmds) != 9 {
		t.Errorf("expected 9 CLI commands, got %d", len(cmds))
	}
	for _, cmd := range cmds {
		if cmd.PluginName != "filetransfer" {
			t.Errorf("command %s has wrong plugin name: %s", cmd.Name, cmd.PluginName)
		}
		if cmd.Run == nil {
			t.Errorf("command %s has nil Run function", cmd.Name)
		}
	}
}

func TestCLICommandRegistration(t *testing.T) {
	// Clean state.
	plugin.UnregisterCLICommands("filetransfer")

	RegisterCLI()

	// Verify all 9 commands are registered.
	for _, name := range []string{"send", "download", "browse", "share", "transfers", "accept", "reject", "cancel", "clean"} {
		cmd, ok := plugin.FindCLICommand(name)
		if !ok {
			t.Errorf("command %q not found after RegisterCLI", name)
			continue
		}
		if cmd.PluginName != "filetransfer" {
			t.Errorf("command %q has wrong plugin name: %s", name, cmd.PluginName)
		}
	}

	// Clean up.
	plugin.UnregisterCLICommands("filetransfer")
}

func TestStatusFieldsNilService(t *testing.T) {
	p := New()
	fields := p.StatusFields()
	if fields != nil {
		t.Errorf("expected nil status fields when transfer service is nil, got %v", fields)
	}
}

func TestParsePeerPath(t *testing.T) {
	tests := []struct {
		input      string
		wantPeer   string
		wantPath   string
	}{
		{"home-server:share-abc/file.txt", "home-server", "share-abc/file.txt"},
		{"12D3KooWx:path", "12D3KooWx", "path"},
		{"a:", "", ""},   // too short before colon
		{"", "", ""},
		{"nocolon", "", ""},
	}
	for _, tt := range tests {
		peer, path := parsePeerPath(tt.input)
		if peer != tt.wantPeer || path != tt.wantPath {
			t.Errorf("parsePeerPath(%q) = (%q, %q), want (%q, %q)",
				tt.input, peer, path, tt.wantPeer, tt.wantPath)
		}
	}
}
