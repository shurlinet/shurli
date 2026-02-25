package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
)

// captureExit overrides the package-level osExit variable so that calls to
// osExit inside fn are intercepted.  It returns the exit code and a boolean
// indicating whether osExit was actually called.
//
// How it works: the replacement panics with an exitSentinel value  - the same
// type defined in exit.go  - which immediately unwinds the call stack (just
// like a real os.Exit would halt the process).  A deferred recover catches
// the sentinel and stores the code.  Any other panic is re-raised.
func captureExit(fn func()) (code int, exited bool) {
	old := osExit
	defer func() { osExit = old }()

	osExit = func(c int) {
		panic(exitSentinel(c))
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				if s, ok := r.(exitSentinel); ok {
					code = int(s)
					exited = true
				} else {
					panic(r) // re-raise non-sentinel panics
				}
			}
		}()
		fn()
	}()
	return code, exited
}

// captureStderr redirects os.Stderr during fn and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = old
	data, _ := io.ReadAll(r)
	return string(data)
}

// ---------------------------------------------------------------------------
// Category 1: Thin runXxx wrappers that call doXxx → osExit(1) on error.
//
// We test the ERROR path by passing --config pointing at a nonexistent file.
// Each wrapper's doXxx function will fail with "config error", the wrapper
// prints "Error: ..." to stderr, and calls osExit(1).
//
// For the SUCCESS path, the doXxx functions already have thorough tests.
// Here we verify the wrapper's plumbing: doXxx success → no osExit.
// ---------------------------------------------------------------------------

func TestRunConfigValidate_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runConfigValidate([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunConfigShow_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runConfigShow([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunConfigRollback_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runConfigRollback([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunConfigApply_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runConfigApply([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunConfigConfirm_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runConfigConfirm([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunAuthAdd_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runAuthAdd([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "fake-id"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunAuthList_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runAuthList([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunAuthRemove_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runAuthRemove([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "fake-id"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunAuthValidate_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runAuthValidate([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayAdd_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayAdd([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "/ip4/1.2.3.4/tcp/7777"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayList_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayList([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayRemove_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayRemove([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "/ip4/1.2.3.4/tcp/7777"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunServiceAdd_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runServiceAdd([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "ssh", "localhost:22"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunServiceList_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runServiceList([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunServiceRemove_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runServiceRemove([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "ssh"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunServiceEnable_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runServiceSetEnabled([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "ssh"}, true)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunServiceDisable_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runServiceSetEnabled([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "ssh"}, false)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunResolve_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runResolve([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "some-peer"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunWhoami_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runWhoami([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunStatus_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runStatus([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayAuthorize_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayAuthorize([]string{"fake-id"}, "/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayDeauthorize_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayDeauthorize([]string{"fake-id"}, "/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayListPeers_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayListPeers("/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayServerConfigValidate_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayServerConfigValidate("/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayServerConfigRollback_Error(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayServerConfigRollback("/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 1 SUCCESS paths: thin wrappers that should NOT call osExit.
// We only test a few representative ones  - the doXxx functions themselves
// are exhaustively tested in their own test files.
// ---------------------------------------------------------------------------

func TestRunConfigValidate_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runConfigValidate([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunConfigShow_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runConfigShow([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunRelayList_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runRelayList([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunServiceList_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runServiceList([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunWhoami_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runWhoami([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunStatus_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runStatus([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunAuthList_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runAuthList([]string{"--config", cfgPath})
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

func TestRunConfigConfirm_Success_NoPending(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	code, exited := captureExit(func() {
		runConfigConfirm([]string{"--config", cfgPath})
	})
	// No pending commit-confirmed → doConfigConfirm returns error → exit(1)
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for no pending config, got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 2: Dispatchers  - test unknown subcommand → osExit(1) and
// empty args → osExit(1).
// ---------------------------------------------------------------------------

func TestRunConfig_EmptyArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runConfig(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunConfig_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runConfig([]string{"bogus"})
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown config command") {
		t.Errorf("stderr should mention unknown command, got: %s", stderr)
	}
}

func TestRunAuth_EmptyArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runAuth(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunAuth_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runAuth([]string{"bogus"})
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown auth command") {
		t.Errorf("stderr should mention unknown command, got: %s", stderr)
	}
}

func TestRunService_EmptyArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runService(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunService_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runService([]string{"bogus"})
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown service command") {
		t.Errorf("stderr should mention unknown command, got: %s", stderr)
	}
}

func TestRunRelay_EmptyArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runRelay(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelay_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runRelay([]string{"bogus"})
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown relay command") {
		t.Errorf("stderr should mention unknown command, got: %s", stderr)
	}
}

func TestRunDaemon_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runDaemon([]string{"bogus"})
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown daemon subcommand") {
		t.Errorf("stderr should mention unknown subcommand, got: %s", stderr)
	}
}

func TestRunRelayServerConfig_EmptyArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayServerConfig(nil, "/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayServerConfig_UnknownSubcommand(t *testing.T) {
	stderr := captureStderr(t, func() {
		code, exited := captureExit(func() {
			runRelayServerConfig([]string{"bogus"}, "/tmp/nonexistent-shurli-test/relay-server.yaml")
		})
		if !exited || code != 1 {
			t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
		}
	})
	if !strings.Contains(stderr, "Unknown config command") {
		t.Errorf("stderr should mention unknown command, got: %s", stderr)
	}
}

// ---------------------------------------------------------------------------
// Category 3: printXxxUsage functions  - just verify they don't panic.
// ---------------------------------------------------------------------------

func TestPrintUsage(t *testing.T) {
	// Redirect stdout to discard (these write to os.Stdout directly)
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printUsage()
}

func TestPrintVersion(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printVersion()
}

func TestPrintDaemonUsage(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printDaemonUsage()
}

func TestPrintAuthUsage(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printAuthUsage()
}

func TestPrintConfigUsage(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printConfigUsage()
}

func TestPrintServiceUsage(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printServiceUsage()
}

func TestPrintRelayServeUsage(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	printRelayServeUsage()
}

// ---------------------------------------------------------------------------
// Category 4: runRelayServerVersion  - pure output, no exit.
// ---------------------------------------------------------------------------

func TestRunRelayServerVersion(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	code, exited := captureExit(func() {
		runRelayServerVersion()
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

// ---------------------------------------------------------------------------
// Category 5: Daemon client commands  - these call daemonClient() which will
// fail because no daemon is running.  Verify they osExit(1).
// ---------------------------------------------------------------------------

func TestRunDaemonStop_NoDaemon(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonStop()
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) when no daemon, got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemonStatus_NoDaemon(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonStatus(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) when no daemon, got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemonPing_NoArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonPing(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for missing peer arg, got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemonConnect_MissingFlags(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonConnect(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for missing flags, got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemonDisconnect_NoArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonDisconnect(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for missing proxy ID, got exited=%v code=%d", exited, code)
	}
}

func TestDaemonClient_NoDaemon(t *testing.T) {
	code, exited := captureExit(func() {
		_ = daemonClient()
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) when no daemon, got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 6: Relay dispatcher routes known subcommands correctly.
// Verify 'relay version' doesn't exit (it's pure output).
// ---------------------------------------------------------------------------

func TestRunRelay_Version(t *testing.T) {
	old := os.Stdout
	os.Stdout = os.NewFile(0, os.DevNull)
	defer func() { os.Stdout = old }()

	code, exited := captureExit(func() {
		runRelay([]string{"version"})
	})
	if exited {
		t.Errorf("relay version should not exit, got code=%d", code)
	}
}

// Verify dispatcher routes to runRelayAdd (which will error on bad config).
func TestRunRelay_Add_Dispatches(t *testing.T) {
	code, exited := captureExit(func() {
		runRelay([]string{"add", "--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "/ip4/1.2.3.4/tcp/7777"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// Verify dispatcher routes to runRelayList (which will error on bad config).
func TestRunRelay_List_Dispatches(t *testing.T) {
	code, exited := captureExit(func() {
		runRelay([]string{"list", "--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// Verify dispatcher routes to runRelayRemove (which will error on bad config).
func TestRunRelay_Remove_Dispatches(t *testing.T) {
	code, exited := captureExit(func() {
		runRelay([]string{"remove", "--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "/ip4/1.2.3.4/tcp/7777"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 7: fatal() error paths  - functions that used log.Fatalf are now
// testable via captureExit. Test early error paths (config loading, flag
// parsing) for all P2P functions.
// ---------------------------------------------------------------------------

// --- runInvite error paths ---

func TestRunInvite_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runInvite([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunInvite_NoRelayAddresses(t *testing.T) {
	// Create a config with empty relay addresses
	dir := t.TempDir()
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	data, _ := crypto.MarshalPrivateKey(priv)
	os.WriteFile(filepath.Join(dir, "identity.key"), data, 0600)
	os.WriteFile(filepath.Join(dir, "authorized_keys"), nil, 0600)

	cfg := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses: []
  reservation_interval: "2m"
discovery:
  rendezvous: "test-network"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"
services: {}
names: {}
`
	cfgPath := filepath.Join(dir, "shurli.yaml")
	os.WriteFile(cfgPath, []byte(cfg), 0600)

	code, exited := captureExit(func() {
		runInvite([]string{"--config", cfgPath})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for no relay addresses, got exited=%v code=%d", exited, code)
	}
}

// --- runJoin error paths ---

func TestRunJoin_NoArgs(t *testing.T) {
	// runJoin with no invite code should print usage and exit
	code, exited := captureExit(func() {
		// Set empty env to ensure no SHURLI_INVITE_CODE
		old := os.Getenv("SHURLI_INVITE_CODE")
		os.Unsetenv("SHURLI_INVITE_CODE")
		defer os.Setenv("SHURLI_INVITE_CODE", old)
		runJoin(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunJoin_InvalidInviteCode(t *testing.T) {
	code, exited := captureExit(func() {
		runJoin([]string{"not-a-valid-invite-code"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// --- runPing error paths ---

func TestRunPing_NoArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runPing(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunPing_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runPing([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "some-peer"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// --- runTraceroute error paths ---

func TestRunTraceroute_NoArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runTraceroute(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunTraceroute_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runTraceroute([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "some-peer"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// --- runProxy error paths ---

func TestRunProxy_NoArgs(t *testing.T) {
	code, exited := captureExit(func() {
		runProxy(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunProxy_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runProxy([]string{"--config", "/tmp/nonexistent-shurli-test/shurli.yaml", "peer", "ssh", "2222"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// --- runRelayServe error paths ---

func TestRunRelayServe_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayServe([]string{"--config", "/tmp/nonexistent-shurli-test/relay-server.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayServe_InvalidConfig(t *testing.T) {
	// Create a config file with invalid YAML
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "relay-server.yaml")
	os.WriteFile(cfgFile, []byte("this: is: not: valid: yaml: [[["), 0600)

	code, exited := captureExit(func() {
		runRelayServe([]string{"--config", cfgFile})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for invalid config, got exited=%v code=%d", exited, code)
	}
}

// --- runRelayInfo error paths ---

func TestRunRelayInfo_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		runRelayInfo("/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayInfo_MissingKeyFile(t *testing.T) {
	// Create a config with a key file that doesn't exist
	dir := t.TempDir()
	cfg := `version: 1
identity:
  key_file: "/tmp/nonexistent-shurli-test/missing.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
resources:
  max_reservations: 128
  max_circuits: 16
  buffer_size: 2048
  max_reservations_per_ip: 8
  max_reservations_per_asn: 32
  reservation_ttl: "1h"
  session_duration: "10m"
  session_data_limit: "64MB"
`
	cfgFile := filepath.Join(dir, "relay-server.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0600)

	code, exited := captureExit(func() {
		runRelayInfo(cfgFile)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for missing key, got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayInfo_InvalidKeyFile(t *testing.T) {
	dir := t.TempDir()
	// Write garbage as key file
	keyFile := filepath.Join(dir, "identity.key")
	os.WriteFile(keyFile, []byte("not a valid key"), 0600)

	cfg := `version: 1
identity:
  key_file: "` + keyFile + `"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
resources:
  max_reservations: 128
  max_circuits: 16
  buffer_size: 2048
  max_reservations_per_ip: 8
  max_reservations_per_asn: 32
  reservation_ttl: "1h"
  session_duration: "10m"
  session_data_limit: "64MB"
`
	cfgFile := filepath.Join(dir, "relay-server.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0600)

	code, exited := captureExit(func() {
		runRelayInfo(cfgFile)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for invalid key, got exited=%v code=%d", exited, code)
	}
}

func TestRunRelayInfo_Success(t *testing.T) {
	cfgFile := writeRelayServerTestConfig(t)

	code, exited := captureExit(func() {
		runRelayInfo(cfgFile)
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
}

// --- loadRelayAuthKeysPath (fatal wrapper) ---

func TestLoadRelayAuthKeysPath_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		loadRelayAuthKeysPath("/tmp/nonexistent-shurli-test/relay-server.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestLoadRelayAuthKeysPath_Success(t *testing.T) {
	cfgFile := writeRelayServerTestConfig(t)

	var path string
	code, exited := captureExit(func() {
		path = loadRelayAuthKeysPath(cfgFile)
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
}

// --- resolveConfigFile (fatal wrapper) ---

func TestResolveConfigFile_ConfigNotFound(t *testing.T) {
	code, exited := captureExit(func() {
		resolveConfigFile("/tmp/nonexistent-shurli-test/shurli.yaml")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestResolveConfigFile_Success(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	var cfg interface{}
	code, exited := captureExit(func() {
		_, c := resolveConfigFile(cfgPath)
		cfg = c
	})
	if exited {
		t.Errorf("should not have exited, got code=%d", code)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

// --- runInit error path (invalid config flag) ---

func TestRunInit_InvalidConfigPath(t *testing.T) {
	// runInit with a non-writable directory should fail
	code, exited := captureExit(func() {
		runInit([]string{"--config", "/proc/nonexistent/shurli.yaml", "--non-interactive"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// --- Relay serve validation error (valid YAML but missing required fields) ---

func TestRunRelayServe_ValidationError(t *testing.T) {
	dir := t.TempDir()
	// Valid YAML but missing identity key_file
	cfg := `version: 1
identity:
  key_file: ""
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: "auth_keys"
  enable_connection_gating: true
resources:
  max_reservations: 128
  max_circuits: 16
  buffer_size: 2048
  max_reservations_per_ip: 8
  max_reservations_per_asn: 32
  reservation_ttl: "1h"
  session_duration: "10m"
  session_data_limit: "64MB"
`
	cfgFile := filepath.Join(dir, "relay-server.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0600)

	code, exited := captureExit(func() {
		runRelayServe([]string{"--config", cfgFile})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for validation error, got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 8: serveRuntime getters  - construct a struct directly and verify
// each getter returns the expected value.
// ---------------------------------------------------------------------------

func TestServeRuntime_Getters(t *testing.T) {
	now := time.Now()
	rt := &serveRuntime{
		network:    nil, // P2P-dependent, just verify it returns nil
		configFile: "/etc/shurli/config.yaml",
		authKeys:   "/etc/shurli/authorized_keys",
		version:    "1.2.3",
		startTime:  now,
		config: &config.HomeNodeConfig{
			Protocols: config.ProtocolsConfig{
				PingPong: config.PingPongConfig{
					ID: "/pingpong/1.0.0",
				},
			},
		},
	}

	if rt.Network() != nil {
		t.Error("Network() should be nil")
	}
	if rt.ConfigFile() != "/etc/shurli/config.yaml" {
		t.Errorf("ConfigFile() = %q", rt.ConfigFile())
	}
	if rt.AuthKeysPath() != "/etc/shurli/authorized_keys" {
		t.Errorf("AuthKeysPath() = %q", rt.AuthKeysPath())
	}
	if rt.Version() != "1.2.3" {
		t.Errorf("Version() = %q", rt.Version())
	}
	if rt.StartTime() != now {
		t.Errorf("StartTime() = %v, want %v", rt.StartTime(), now)
	}
	if rt.PingProtocolID() != "/pingpong/1.0.0" {
		t.Errorf("PingProtocolID() = %q", rt.PingProtocolID())
	}
}

func TestServeRuntime_GaterForHotReload_NilGater(t *testing.T) {
	rt := &serveRuntime{
		gater:    nil,
		authKeys: "/etc/shurli/authorized_keys",
	}
	if rt.GaterForHotReload() != nil {
		t.Error("GaterForHotReload() should return nil when gater is nil")
	}
}

func TestServeRuntime_GaterForHotReload_EmptyAuthKeys(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	rt := &serveRuntime{
		gater:    gater,
		authKeys: "",
	}
	if rt.GaterForHotReload() != nil {
		t.Error("GaterForHotReload() should return nil when authKeys is empty")
	}
}

func TestServeRuntime_GaterForHotReload_Valid(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	rt := &serveRuntime{
		gater:    gater,
		authKeys: "/etc/shurli/authorized_keys",
	}
	reloader := rt.GaterForHotReload()
	if reloader == nil {
		t.Fatal("GaterForHotReload() should return non-nil")
	}
}

// ---------------------------------------------------------------------------
// Category 9: gaterReloader.ReloadFromFile  - test with real authorized_keys.
// ---------------------------------------------------------------------------

func TestGaterReloader_ReloadFromFile(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "authorized_keys")

	// Generate a test peer ID
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	// Write authorized_keys with the peer ID (format: PEERID # comment)
	content := id.String() + " # test-peer\n"
	if err := os.WriteFile(authFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	reloader := &gaterReloader{gater: gater, authKeysPath: authFile}

	if err := reloader.ReloadFromFile(); err != nil {
		t.Fatalf("ReloadFromFile() error: %v", err)
	}

	// Verify the peer was loaded
	if !gater.IsAuthorized(id) {
		t.Error("expected peer to be authorized after reload")
	}
}

func TestGaterReloader_ReloadFromFile_MissingFile(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	reloader := &gaterReloader{gater: gater, authKeysPath: "/tmp/nonexistent-shurli-test/authorized_keys"}

	err := reloader.ReloadFromFile()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "failed to reload authorized_keys") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 10: loadOrCreateConfig  - test existing config path.
// ---------------------------------------------------------------------------

func TestLoadOrCreateConfig_ExistingConfig(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	var cfgFile string
	var cfg *config.NodeConfig
	var configDir string
	var created bool
	code, exited := captureExit(func() {
		cfgFile, cfg, configDir, created = loadOrCreateConfig(cfgPath, "", "")
	})
	if exited {
		t.Fatalf("should not have exited, got code=%d", code)
	}
	if created {
		t.Error("expected created=false for existing config")
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfgFile != cfgPath {
		t.Errorf("cfgFile = %q, want %q", cfgFile, cfgPath)
	}
	if configDir == "" {
		t.Error("expected non-empty configDir")
	}
}

func TestLoadOrCreateConfig_InvalidConfig(t *testing.T) {
	// Use os.MkdirTemp instead of t.TempDir() to avoid a Linux-specific
	// TempDir cleanup race with the panic/recover in captureExit (the
	// runtime's openat fd can go stale after the panic unwind).
	dir, err := os.MkdirTemp("", "TestLoadOrCreateConfig_InvalidConfig")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cfgPath := filepath.Join(dir, "shurli.yaml")
	os.WriteFile(cfgPath, []byte("this: is: bad: yaml: [[["), 0600)

	code, exited := captureExit(func() {
		loadOrCreateConfig(cfgPath, "", "")
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) for invalid config, got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 11: runDaemonServices and runDaemonPeers  - no daemon running.
// ---------------------------------------------------------------------------

func TestRunDaemonServices_NoDaemon(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonServices(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) when no daemon, got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemonPeers_NoDaemon(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemonPeers(nil)
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1) when no daemon, got exited=%v code=%d", exited, code)
	}
}

// ---------------------------------------------------------------------------
// Category 12: runDaemon "start" dispatch  - config not found.
// ---------------------------------------------------------------------------

func TestRunDaemon_StartDispatch_ConfigError(t *testing.T) {
	code, exited := captureExit(func() {
		runDaemon([]string{"start", "--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

func TestRunDaemon_EmptyArgs_ConfigError(t *testing.T) {
	// runDaemon with no args calls runDaemonStart which needs a config.
	// With no config available in test env, it should eventually exit.
	// But DefaultConfigDir might find a real config, so use explicit path.
	code, exited := captureExit(func() {
		runDaemon([]string{"start", "--config", "/tmp/nonexistent-shurli-test/shurli.yaml"})
	})
	if !exited || code != 1 {
		t.Errorf("expected exit(1), got exited=%v code=%d", exited, code)
	}
}

// nodeConfigTemplate already tested in cmd_join_test.go
