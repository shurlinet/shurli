//go:build integration

// Package docker_test contains Docker-based integration tests for Shurli.
//
// These tests verify the actual compiled binaries work end-to-end in separate
// containers communicating through a relay server. They are NOT run by
// regular "go test ./..." - use "go test -tags integration ./test/docker/".
//
// Prerequisites:
//   - Docker and Docker Compose installed
//   - Ports in the 172.28.0.0/24 range available (Docker bridge network)
package docker_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// composePath is the absolute path to the compose.yaml file.
var composePath string

// relayPeerID is extracted from relay logs during setup and used by all tests.
var relayPeerID string

// relayMultiaddr is the full multiaddr for the relay server.
var relayMultiaddr string

// nodeAPeerID is set during the invite/join test for use by later tests.
var nodeAPeerID string

// nodeBPeerID is set during the invite/join test for use by later tests.
var nodeBPeerID string

func TestMain(m *testing.M) {
	// Resolve compose file path relative to this test file.
	composePath = findComposePath()

	// Bring up the Docker Compose environment.
	if err := composeUp(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up failed: %v\n", err)
		composeDown() // clean up partial state
		os.Exit(1)
	}

	// Write relay config into the relay container and restart it.
	if err := setupRelay(); err != nil {
		fmt.Fprintf(os.Stderr, "relay setup failed: %v\n", err)
		composeLogs() // dump logs for debugging
		composeDown()
		os.Exit(1)
	}

	// Extract relay peer ID from logs.
	pid, err := extractRelayPeerID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to extract relay peer ID: %v\n", err)
		composeLogs()
		composeDown()
		os.Exit(1)
	}
	relayPeerID = pid
	relayMultiaddr = fmt.Sprintf("/ip4/172.28.0.10/tcp/7777/p2p/%s", relayPeerID)

	fmt.Printf("Relay Peer ID: %s...\n", relayPeerID[:16])
	fmt.Printf("Relay Multiaddr: (redacted for CI logs)\n")

	// Run tests.
	code := m.Run()

	// Collect coverage data from containers before teardown.
	collectDockerCoverage()

	// Tear down.
	composeDown()
	os.Exit(code)
}

// ─── Test Cases ───────────────────────────────────────────────────────────────

func TestRelayHealthy(t *testing.T) {
	// Verify relay is running and config is valid.
	out, _, err := dockerExec("relay", "sh", "-c", "cd /data && shurli relay config validate")
	if err != nil {
		t.Fatalf("relay config validate failed: %v\noutput: %s", err, out)
	}
	t.Logf("relay config validate output: %q", strings.TrimSpace(out))

	// Verify we extracted a valid peer ID (starts with 12D3KooW for Ed25519).
	if !strings.HasPrefix(relayPeerID, "12D3KooW") {
		t.Fatalf("relay peer ID doesn't look valid: %q", relayPeerID)
	}

	// Verify relay process is actually running.
	ps, _, err := dockerExec("relay", "sh", "-c", "ps aux | grep 'shurli relay serve' | grep -v grep")
	if err != nil || !strings.Contains(ps, "shurli") {
		t.Fatalf("shurli relay serve process not running in container.\nps output: %s", ps)
	}
}

func TestInviteJoinFlow(t *testing.T) {
	// ── Step 1: Set up node-a ──
	t.Log("Setting up node-a...")
	if err := setupNode("node-a", relayMultiaddr); err != nil {
		t.Fatalf("node-a setup failed: %v", err)
	}

	// Get node-a peer ID.
	out, _, err := dockerExec("node-a", "shurli", "whoami", "--config", "/root/.config/shurli/config.yaml")
	if err != nil {
		t.Fatalf("node-a whoami failed: %v", err)
	}
	nodeAPeerID = strings.TrimSpace(out)
	t.Logf("Node-A Peer ID: %s", nodeAPeerID)

	// ── Step 2: Set up node-b ──
	t.Log("Setting up node-b...")
	if err := setupNode("node-b", relayMultiaddr); err != nil {
		t.Fatalf("node-b setup failed: %v", err)
	}

	out, _, err = dockerExec("node-b", "shurli", "whoami", "--config", "/root/.config/shurli/config.yaml")
	if err != nil {
		t.Fatalf("node-b whoami failed: %v", err)
	}
	nodeBPeerID = strings.TrimSpace(out)
	t.Logf("Node-B Peer ID: %s", nodeBPeerID)

	// ── Step 3: Run invite on node-a (background) ──
	t.Log("Starting invite on node-a...")
	// Run invite in background, capturing stdout (the invite code) to a file.
	// No nohup needed - container runs "sleep infinity" so no SIGHUP risk.
	_, _, err = dockerExec("node-a", "sh", "-c",
		"shurli invite --non-interactive --name home --config /root/.config/shurli/config.yaml > /tmp/invite-stdout.txt 2>/tmp/invite-stderr.txt &")
	if err != nil {
		t.Fatalf("failed to start invite on node-a: %v", err)
	}

	// ── Step 4: Poll for invite code ──
	t.Log("Waiting for invite code...")
	var inviteCode string
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, _, err := dockerExec("node-a", "cat", "/tmp/invite-stdout.txt")
		if err == nil {
			code := strings.TrimSpace(out)
			// Invite codes are base32, typically 200+ characters.
			if len(code) > 100 {
				inviteCode = code
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	if inviteCode == "" {
		// Dump stderr for debugging.
		stderr, _, _ := dockerExec("node-a", "cat", "/tmp/invite-stderr.txt")
		t.Fatalf("invite code not generated within timeout.\nInvite stderr:\n%s", stderr)
	}
	t.Logf("Invite code length: %d chars", len(inviteCode))
	t.Logf("Invite code first 40 chars: %q", inviteCode[:min(40, len(inviteCode))])

	// ── Step 5: Run join on node-b ──
	t.Log("Running join on node-b...")
	// Use SHURLI_INVITE_CODE env var - clean approach for scripted usage.
	joinCmd := fmt.Sprintf("SHURLI_INVITE_CODE='%s' shurli join --non-interactive --name laptop", inviteCode)
	out, stderr, err := dockerExecWithTimeout("node-b", 60*time.Second, "sh", "-c", joinCmd)
	if err != nil {
		t.Fatalf("node-b join failed: %v\nstdout: %s\nstderr: %s", err, out, stderr)
	}
	t.Logf("Join output (stderr): %s", stderr)

	// ── Step 6: Verify authorized_keys on both nodes ──
	t.Log("Verifying authorized_keys...")

	// Node-a should have node-b's peer ID.
	authA, _, err := dockerExec("node-a", "cat", "/root/.config/shurli/authorized_keys")
	if err != nil {
		t.Fatalf("failed to read node-a authorized_keys: %v", err)
	}
	if !strings.Contains(authA, nodeBPeerID) {
		t.Errorf("node-a authorized_keys missing node-b peer ID.\nExpected to contain: %s\nGot:\n%s", nodeBPeerID, authA)
	}

	// Node-b should have node-a's peer ID.
	authB, _, err := dockerExec("node-b", "cat", "/root/.config/shurli/authorized_keys")
	if err != nil {
		t.Fatalf("failed to read node-b authorized_keys: %v", err)
	}
	if !strings.Contains(authB, nodeAPeerID) {
		t.Errorf("node-b authorized_keys missing node-a peer ID.\nExpected to contain: %s\nGot:\n%s", nodeAPeerID, authB)
	}

	// ── Step 7: Verify names in node-b's config ──
	cfgB, _, err := dockerExec("node-b", "cat", "/root/.config/shurli/config.yaml")
	if err != nil {
		t.Fatalf("failed to read node-b config: %v", err)
	}
	if !strings.Contains(cfgB, "home:") || !strings.Contains(cfgB, nodeAPeerID) {
		t.Errorf("node-b config missing name mapping for 'home'.\nGot:\n%s", cfgB)
	}

	t.Log("Invite/join flow verified successfully.")
}

func TestPingThroughRelay(t *testing.T) {
	if nodeAPeerID == "" || nodeBPeerID == "" {
		t.Skip("Skipping: invite/join flow must complete first (peer IDs not set)")
	}

	// ── Step 1: Start daemon on node-a (background) ──
	// force_private_reachability is already true in the test config,
	// so node-a will only advertise relay addresses.
	t.Log("Starting daemon on node-a...")
	_, _, err := dockerExec("node-a", "sh", "-c",
		"shurli daemon start --config /root/.config/shurli/config.yaml > /tmp/daemon-stdout.txt 2>&1 &")
	if err != nil {
		t.Fatalf("failed to start daemon on node-a: %v", err)
	}

	// ── Step 3: Wait for daemon to get relay reservation ──
	t.Log("Waiting for daemon relay reservation...")
	deadline := time.Now().Add(30 * time.Second)
	daemonReady := false
	for time.Now().Before(deadline) {
		out, _, _ := dockerExec("node-a", "cat", "/tmp/daemon-stdout.txt")
		if strings.Contains(out, "Relay address:") || strings.Contains(out, "relay reservation") ||
			strings.Contains(out, "p2p-circuit") || strings.Contains(out, "listening") {
			daemonReady = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !daemonReady {
		daemonLog, _, _ := dockerExec("node-a", "cat", "/tmp/daemon-stdout.txt")
		t.Fatalf("daemon did not establish relay reservation within timeout.\nDaemon log:\n%s", daemonLog)
	}

	// Give the daemon a moment to fully register protocols after relay reservation.
	time.Sleep(3 * time.Second)

	// ── Step 4: Ping from node-b ──
	t.Log("Pinging node-a from node-b...")
	// Config is at default location (~/.config/shurli/config.yaml), no --config needed.
	out, stderr, err := dockerExecWithTimeout("node-b", 90*time.Second,
		"shurli", "ping", "--json", "-c", "2", "home")
	if err != nil {
		t.Fatalf("node-b ping failed: %v\nstdout: %s\nstderr: %s", err, out, stderr)
	}

	// ── Step 5: Parse and verify JSON output ──
	t.Log("Verifying ping results...")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 JSON lines (2 pings + 1 stats), got %d:\n%s", len(lines), out)
	}

	// Parse individual ping results.
	type pingResult struct {
		Seq    int     `json:"seq"`
		PeerID string  `json:"peer_id"`
		RttMs  float64 `json:"rtt_ms"`
		Path   string  `json:"path"`
		Error  string  `json:"error"`
	}

	var pings []pingResult
	for _, line := range lines[:len(lines)-1] { // last line is stats
		var r pingResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // might be a stats line parsed early
		}
		if r.Seq > 0 {
			pings = append(pings, r)
		}
	}

	if len(pings) < 2 {
		t.Fatalf("expected 2 ping results, got %d. Full output:\n%s", len(pings), out)
	}

	for i, p := range pings {
		if p.Error != "" {
			t.Errorf("ping %d: unexpected error: %s", i+1, p.Error)
		}
		if p.RttMs <= 0 {
			t.Errorf("ping %d: RTT should be positive, got %.3f", i+1, p.RttMs)
		}
		if p.Path != "RELAYED" && p.Path != "DIRECT" {
			t.Errorf("ping %d: unexpected path %q (expected RELAYED or DIRECT)", i+1, p.Path)
		}
	}

	// Parse stats (last line).
	type pingStats struct {
		Sent     int     `json:"sent"`
		Received int     `json:"received"`
		LossPct  float64 `json:"loss_pct"`
	}
	var stats pingStats
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &stats); err != nil {
		t.Logf("Warning: could not parse stats line: %v", err)
	} else {
		if stats.Received < 2 {
			t.Errorf("expected at least 2 received pings, got %d (loss: %.0f%%)", stats.Received, stats.LossPct)
		}
	}

	t.Log("Ping through relay verified successfully.")
}

// ─── Coverage Collection ─────────────────────────────────────────────────────

// collectDockerCoverage gracefully stops all shurli processes inside Docker
// containers so they flush coverage data (GOCOVERDIR=/covdata), then copies
// the data to a host directory for merging with unit test coverage.
//
// Set SHURLI_COVDIR to enable collection. Example:
//
//	SHURLI_COVDIR=./coverage/integration go test -tags integration ./test/docker/
//
// Then merge with unit coverage:
//
//	go test -cover ./... -args -test.gocoverdir=./coverage/unit
//	go tool covdata merge -i=./coverage/unit,./coverage/integration -o=./coverage/merged
//	go tool covdata textfmt -i=./coverage/merged -o=./coverage/combined.out
//	go tool cover -func=./coverage/combined.out | tail -1
func collectDockerCoverage() {
	covDir := os.Getenv("SHURLI_COVDIR")
	if covDir == "" {
		fmt.Println("SHURLI_COVDIR not set, skipping coverage collection.")
		return
	}

	if err := os.MkdirAll(covDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create coverage dir %s: %v\n", covDir, err)
		return
	}

	fmt.Println("=== Collecting Docker coverage data ===")

	containers := []string{"relay", "node-a", "node-b"}

	// Send SIGTERM to all shurli processes so they flush coverage on exit.
	// The container PID 1 is "sleep infinity", so we target shurli specifically.
	for _, c := range containers {
		dockerExec(c, "sh", "-c", "pkill -TERM shurli 2>/dev/null || true")
	}

	// Wait for processes to exit and flush coverage.
	time.Sleep(3 * time.Second)

	// Copy coverage data from each container.
	for _, c := range containers {
		cmd := exec.Command("docker", "cp", c+":/covdata/.", covDir+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "docker cp %s:/covdata failed: %v (%s)\n", c, err, out)
		} else {
			fmt.Printf("Collected coverage from %s\n", c)
		}
	}

	// List what we collected.
	entries, _ := os.ReadDir(covDir)
	fmt.Printf("Coverage files collected: %d\n", len(entries))
}

// ─── Docker Compose Helpers ───────────────────────────────────────────────────

func findComposePath() string {
	// Try relative to test file location.
	candidates := []string{
		"compose.yaml",
		"test/docker/compose.yaml",
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	// Fallback: look relative to the module root.
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err == nil {
		modRoot := filepath.Dir(strings.TrimSpace(string(out)))
		p := filepath.Join(modRoot, "test", "docker", "compose.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Last resort.
	return "test/docker/compose.yaml"
}

func composeCmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"compose", "-f", composePath}, args...)
	cmd := exec.Command("docker", fullArgs...)
	cmd.Dir = filepath.Dir(composePath)
	return cmd
}

func composeUp() error {
	cmd := composeCmd("up", "--build", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Println("=== docker compose up --build -d ===")
	return cmd.Run()
}

func composeDown() {
	cmd := composeCmd("down", "-v", "--remove-orphans")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Println("=== docker compose down -v ===")
	cmd.Run()
}

func composeLogs() {
	// Dump relay process logs (shurli relay serve runs inside a sleeping container).
	fmt.Fprintln(os.Stderr, "=== relay server logs ===")
	out, _, _ := dockerExec("relay", "cat", "/tmp/relay-stdout.txt")
	fmt.Fprintln(os.Stderr, out)

	// Dump compose-level logs for container lifecycle info.
	fmt.Fprintln(os.Stderr, "=== docker compose logs ===")
	cmd := composeCmd("logs", "--no-color")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Run()
}

// ─── Container Helpers ────────────────────────────────────────────────────────

func dockerExec(container string, args ...string) (stdout, stderr string, err error) {
	return dockerExecWithTimeout(container, 30*time.Second, args...)
}

func dockerExecWithTimeout(container string, timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	fullArgs := append([]string{"exec", container}, args...)
	cmd := exec.Command("docker", fullArgs...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Use a done channel for timeout.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err = <-done:
		return outBuf.String(), errBuf.String(), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return outBuf.String(), errBuf.String(), fmt.Errorf("docker exec timed out after %s", timeout)
	}
}

func writeFileInContainer(container, path, content string) error {
	cmd := exec.Command("docker", "exec", "-i", container, "sh", "-c", fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// ─── Setup Helpers ────────────────────────────────────────────────────────────

func setupRelay() error {
	cfg := generateRelayConfig()

	// Write config into relay container.
	if err := writeFileInContainer("relay", "/data/relay-server.yaml", cfg); err != nil {
		return fmt.Errorf("failed to write relay config: %w", err)
	}
	// Security hardening requires 0600 on config files.
	if _, _, err := dockerExec("relay", "chmod", "600", "/data/relay-server.yaml"); err != nil {
		return fmt.Errorf("failed to chmod relay config: %w", err)
	}

	// Create empty authorized_keys file.
	if err := writeFileInContainer("relay", "/data/relay_authorized_keys", "# test relay - gating disabled\n"); err != nil {
		return fmt.Errorf("failed to write relay authorized_keys: %w", err)
	}

	// Start relay server in background inside the container.
	// Container is sleeping; we launch shurli relay serve as a background process.
	_, _, err := dockerExec("relay", "sh", "-c",
		"cd /data && nohup shurli relay serve > /tmp/relay-stdout.txt 2>&1 &")
	if err != nil {
		return fmt.Errorf("failed to start relay server: %w", err)
	}

	fmt.Println("Waiting for relay to start...")
	return nil
}

func extractRelayPeerID() (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	re := regexp.MustCompile(`Relay Peer ID: (12D3KooW\S+)`)

	for time.Now().Before(deadline) {
		out, _, err := dockerExec("relay", "cat", "/tmp/relay-stdout.txt")
		if err == nil {
			matches := re.FindStringSubmatch(out)
			if len(matches) >= 2 {
				return matches[1], nil
			}
		}
		time.Sleep(2 * time.Second)
	}

	// Dump what we have for debugging.
	out, _, _ := dockerExec("relay", "cat", "/tmp/relay-stdout.txt")
	return "", fmt.Errorf("could not find relay peer ID in logs within 30s.\nRelay output:\n%s", out)
}

func setupNode(container, relayAddr string) error {
	cfg := generateNodeConfig(relayAddr)

	// Write config.
	if err := writeFileInContainer(container, "/root/.config/shurli/config.yaml", cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	// Security hardening requires 0600 on config files.
	if _, _, err := dockerExec(container, "chmod", "600", "/root/.config/shurli/config.yaml"); err != nil {
		return fmt.Errorf("failed to chmod node config: %w", err)
	}

	// Write empty authorized_keys.
	if err := writeFileInContainer(container, "/root/.config/shurli/authorized_keys",
		"# authorized_keys - Peer ID allowlist (one per line)\n"); err != nil {
		return fmt.Errorf("failed to write authorized_keys: %w", err)
	}

	return nil
}

// ─── Config Generators ────────────────────────────────────────────────────────

// generateRelayConfig returns a minimal relay config for testing.
// Connection gating is DISABLED so we don't need to pre-authorize node peer IDs.
func generateRelayConfig() string {
	return `# Relay config for integration testing (gating disabled)
version: 1

identity:
  key_file: "relay_node.key"

network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"

security:
  authorized_keys_file: "relay_authorized_keys"
  enable_connection_gating: false

health:
  enabled: true
  listen_address: "127.0.0.1:9090"
`
}

// generateNodeConfig returns a node config pointing to the given relay.
// Matches the structure from config_template.go.
func generateNodeConfig(relayAddr string) string {
	return fmt.Sprintf(`# Shurli configuration
# Generated by: integration test

version: 1

identity:
  key_file: "identity.key"

network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
    - "/ip4/0.0.0.0/udp/0/quic-v1"
  force_private_reachability: true

relay:
  addresses:
    - "%s"
  reservation_interval: "2m"

discovery:
  rendezvous: "shurli-integration-test"
  bootstrap_peers: []

security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true

protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"

names: {}
`, relayAddr)
}
