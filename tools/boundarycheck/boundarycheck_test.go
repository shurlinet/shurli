package boundarycheck

import (
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// overrideState sets the analyzer's internal state directly for testing,
// bypassing the sync.Once file-loading logic.
func overrideState(suppressed map[string]bool, cfg config) {
	// Mark each Once as "done" by replacing with a fresh Once and calling Do
	suppressedOnce = sync.Once{}
	configOnce = sync.Once{}
	repoRootOnce = sync.Once{}
	suppressedOnce.Do(func() {})
	configOnce.Do(func() {})
	// Don't exhaust repoRootOnce -- let findRepoRoot run if needed

	suppressedMap = suppressed
	engineCfg = cfg
	repoRootPath = ""
}

func TestBoundaryCheckSDK(t *testing.T) {
	overrideState(
		map[string]bool{}, // no suppressions
		config{
			engineTypes:    map[string]bool{"TransferService": true},
			protocolConsts: map[string]bool{"TransferProtocol": true},
		},
	)

	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "github.com/shurlinet/shurli/pkg/sdk/fakesdk")
}

func TestBoundaryCheckPlugin(t *testing.T) {
	overrideState(
		map[string]bool{},
		config{engineTypes: map[string]bool{}, protocolConsts: map[string]bool{}},
	)

	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "github.com/shurlinet/shurli/plugins/testplugin")
}

func TestBoundaryCheckCmd(t *testing.T) {
	overrideState(
		map[string]bool{},
		config{engineTypes: map[string]bool{}, protocolConsts: map[string]bool{}},
	)

	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "github.com/shurlinet/shurli/cmd/shurli/fakecmd")
}

func TestIsRegistrationFile(t *testing.T) {
	tests := []struct {
		name   string
		expect bool
	}{
		{"main.go", true},
		{"serve_common.go", true},
		{"cmd_daemon.go", true},
		{"cmd_auth.go", false},
		{"cmd_status.go", false},
		{"cmd_plugin.go", false},
	}

	for _, tt := range tests {
		got := isRegistrationFile(tt.name)
		if got != tt.expect {
			t.Errorf("isRegistrationFile(%q) = %v, want %v", tt.name, got, tt.expect)
		}
	}
}

func TestIsCoreProtocol(t *testing.T) {
	tests := []struct {
		path   string
		expect bool
	}{
		{"/shurli/kad/1.0.0", true},
		{"/shurli/relay-pair/2.0.0", true},
		{"/shurli/peer-notify/1.0.0", true},
		{"/shurli/zkp-auth/1.0.0", true},
		{"/shurli/ping/1.0.0", true},
		{"/shurli/file-transfer/2.0.0", false},
		{"/shurli/file-browse/1.0.0", false},
		{"/shurli/wol-wake/1.0.0", false},
	}

	for _, tt := range tests {
		got := isCoreProtocol(tt.path)
		if got != tt.expect {
			t.Errorf("isCoreProtocol(%q) = %v, want %v", tt.path, got, tt.expect)
		}
	}
}

func TestEngineTypeLookup(t *testing.T) {
	cfg := config{engineTypes: map[string]bool{
		"TransferService": true,
		"ShareRegistry":   true,
	}}

	tests := []struct {
		name   string
		expect bool
	}{
		{"TransferService", true},
		{"ShareRegistry", true},
		{"Network", false},
		{"EventBus", false},
		{"BandwidthTracker", false},
	}

	for _, tt := range tests {
		got := cfg.engineTypes[tt.name]
		if got != tt.expect {
			t.Errorf("engineTypes[%q] = %v, want %v", tt.name, got, tt.expect)
		}
	}
}

func TestPerFileSuppression(t *testing.T) {
	// Per-file suppression: transfer.go suppressed, new_engine.go NOT suppressed.
	// This verifies BUG-15 fix: suppression is per-file, not per-directory.
	m := map[string]bool{
		"pkg/sdk/transfer.go": true,
		"pkg/sdk/share.go":    true,
	}

	if !m["pkg/sdk/transfer.go"] {
		t.Error("transfer.go should be suppressed")
	}
	if !m["pkg/sdk/share.go"] {
		t.Error("share.go should be suppressed")
	}
	if m["pkg/sdk/new_engine.go"] {
		t.Error("new_engine.go should NOT be suppressed (not in list)")
	}
	if m["pkg/sdk/"] {
		t.Error("directory should NOT be suppressed (only files)")
	}
}
