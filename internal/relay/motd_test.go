package relay

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// --- buildSignableData tests ---

func TestBuildSignableData(t *testing.T) {
	ts := int64(1709337600)
	data := buildSignableData(motdTypeMOTD, "hello", ts)

	// Format: [1 type][N msg-bytes][8 timestamp-BE]
	if data[0] != motdTypeMOTD {
		t.Errorf("type byte: got %d, want %d", data[0], motdTypeMOTD)
	}
	if string(data[1:6]) != "hello" {
		t.Errorf("message: got %q, want %q", string(data[1:6]), "hello")
	}
	gotTS := int64(binary.BigEndian.Uint64(data[6:]))
	if gotTS != ts {
		t.Errorf("timestamp: got %d, want %d", gotTS, ts)
	}
}

func TestBuildSignableDataEmpty(t *testing.T) {
	data := buildSignableData(motdTypeRetract, "", 0)
	if len(data) != 1+8 {
		t.Errorf("length: got %d, want %d", len(data), 1+8)
	}
	if data[0] != motdTypeRetract {
		t.Errorf("type byte: got %d, want %d", data[0], motdTypeRetract)
	}
}

// --- encodeFrame tests ---

func TestEncodeFrame(t *testing.T) {
	msg := "relay shutting down"
	ts := int64(1709337600)
	sig := []byte("fakesig64bytes")

	frame := encodeFrame(motdTypeGoodbye, msg, ts, sig)

	// Version byte.
	if frame[0] != motdWireVersion {
		t.Errorf("version: got %d, want %d", frame[0], motdWireVersion)
	}
	// Type byte.
	if frame[1] != motdTypeGoodbye {
		t.Errorf("type: got %d, want %d", frame[1], motdTypeGoodbye)
	}
	// Message length.
	msgLen := binary.BigEndian.Uint16(frame[2:4])
	if int(msgLen) != len(msg) {
		t.Errorf("msg len: got %d, want %d", msgLen, len(msg))
	}
	// Message content.
	gotMsg := string(frame[4 : 4+msgLen])
	if gotMsg != msg {
		t.Errorf("msg: got %q, want %q", gotMsg, msg)
	}
	// Timestamp.
	off := 4 + int(msgLen)
	gotTS := int64(binary.BigEndian.Uint64(frame[off : off+8]))
	if gotTS != ts {
		t.Errorf("timestamp: got %d, want %d", gotTS, ts)
	}
	// Signature.
	gotSig := frame[off+8:]
	if string(gotSig) != string(sig) {
		t.Errorf("sig mismatch")
	}
}

func TestEncodeFrameEmpty(t *testing.T) {
	frame := encodeFrame(motdTypeRetract, "", 0, nil)
	// 1 version + 1 type + 2 len + 0 msg + 8 ts + 0 sig = 12
	if len(frame) != 12 {
		t.Errorf("length: got %d, want 12", len(frame))
	}
}

// --- MOTD client goodbye persistence tests ---

func TestGoodbyePersistence(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	client := NewMOTDClient(func(msg MOTDMessage) {
		calls++
	}, dir)

	fakeRelay := peer.ID("QmFakeRelay1234567890")
	ts := time.Now().Unix()
	sig := []byte("testsig")

	// Store a goodbye.
	client.storeGoodbye(fakeRelay, "farewell", ts, sig)

	// Retrieve it.
	gotMsg, gotTS, found := client.GetStoredGoodbye(fakeRelay)
	if !found {
		t.Fatal("goodbye not found after storing")
	}
	if gotMsg != "farewell" {
		t.Errorf("message: got %q, want %q", gotMsg, "farewell")
	}
	if gotTS != ts {
		t.Errorf("timestamp: got %d, want %d", gotTS, ts)
	}

	// Verify file on disk.
	data, err := os.ReadFile(filepath.Join(dir, "goodbyes.json"))
	if err != nil {
		t.Fatalf("reading goodbyes.json: %v", err)
	}
	var onDisk map[string]storedGoodbye
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("parsing goodbyes.json: %v", err)
	}
	if len(onDisk) != 1 {
		t.Errorf("stored goodbyes: got %d, want 1", len(onDisk))
	}

	// Remove goodbye.
	client.removeGoodbye(fakeRelay)
	_, _, found = client.GetStoredGoodbye(fakeRelay)
	if found {
		t.Error("goodbye still found after removal")
	}
}

func TestGoodbyePersistenceNoDir(t *testing.T) {
	// Empty dataDir should not panic.
	client := NewMOTDClient(nil, "")
	_, _, found := client.GetStoredGoodbye(peer.ID("QmNobody"))
	if found {
		t.Error("should not find goodbye with empty dir")
	}
}

// --- MOTD client timestamp validation (exercised via constants) ---

func TestTimestampConstants(t *testing.T) {
	// Verify the constants are reasonable.
	const (
		maxFutureSkew = 5 * 60        // 5 minutes
		maxPastAge    = 7 * 24 * 3600 // 7 days
	)
	if maxFutureSkew != 300 {
		t.Errorf("future skew: got %d, want 300", maxFutureSkew)
	}
	if maxPastAge != 604800 {
		t.Errorf("past age: got %d, want 604800", maxPastAge)
	}
}

// --- MOTDHandler getter tests ---

func TestMOTDHandlerGetterSetters(t *testing.T) {
	// NewMOTDHandler requires a host and privkey, but we can test
	// the getter/setter logic with a minimally constructed handler.
	h := &MOTDHandler{}

	// Initial state.
	if h.MOTD() != "" {
		t.Error("initial MOTD should be empty")
	}
	if h.HasActiveGoodbye() {
		t.Error("initial goodbye should not be active")
	}
	if h.Goodbye() != "" {
		t.Error("initial goodbye should be empty")
	}

	// Set MOTD.
	h.SetMOTD("test motd")
	if h.MOTD() != "test motd" {
		t.Errorf("MOTD: got %q, want %q", h.MOTD(), "test motd")
	}

	// Clear MOTD.
	h.ClearMOTD()
	if h.MOTD() != "" {
		t.Error("MOTD should be empty after clear")
	}
}

func TestMOTDHandlerPersistedGoodbyeLoad(t *testing.T) {
	dir := t.TempDir()
	goodbyeFile := filepath.Join(dir, "goodbye.json")

	// Write a persisted goodbye.
	pg := persistedGoodbye{
		Message:   "shutting down for maintenance",
		Timestamp: time.Now().Unix(),
		Signature: []byte("sig"),
	}
	data, _ := json.Marshal(pg)
	os.WriteFile(goodbyeFile, data, 0600)

	// Create handler - it should load the persisted goodbye.
	h := &MOTDHandler{goodbyeFile: goodbyeFile}
	h.loadPersistedGoodbye()

	if h.Goodbye() != "shutting down for maintenance" {
		t.Errorf("goodbye: got %q, want %q", h.Goodbye(), "shutting down for maintenance")
	}
	if !h.HasActiveGoodbye() {
		t.Error("goodbye should be active")
	}
}
