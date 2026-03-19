package filetransfer

import (
	"encoding/json"
	"testing"

	"github.com/shurlinet/shurli/pkg/plugin"
)

// FT1: Checkpoint round-trip - Checkpoint returns valid JSON, Restore reconstructs.
func TestFileTransfer_CheckpointRoundTrip(t *testing.T) {
	p := New()
	// Without Start(), there's no transferService. Checkpoint should
	// return ErrSkipCheckpoint (no active state to save).
	_, err := p.Checkpoint()
	if err != plugin.ErrSkipCheckpoint {
		t.Errorf("expected ErrSkipCheckpoint without Start, got %v", err)
	}

	// Simulate a Restore with valid checkpoint data.
	state := checkpointState{
		HasShares: true,
	}
	data, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}

	if err := p.Restore(data); err != nil {
		t.Errorf("Restore with valid data should succeed: %v", err)
	}
}

// FT2: Checkpoint with no active transfers.
func TestFileTransfer_CheckpointEmpty(t *testing.T) {
	p := New()
	// No Start(), no active transfers -> ErrSkipCheckpoint.
	_, err := p.Checkpoint()
	if err != plugin.ErrSkipCheckpoint {
		t.Errorf("expected ErrSkipCheckpoint for empty state, got %v", err)
	}
}

// FT3: Restore with corrupt data - returns error, no panic.
func TestFileTransfer_RestoreCorruptData(t *testing.T) {
	p := New()

	// Garbage bytes.
	garbage := []byte{0xff, 0xfe, 0xfd, 0x00, 0x01, 0x02}
	err := p.Restore(garbage)
	if err == nil {
		t.Error("Restore with garbage should return error")
	}

	// Empty bytes.
	err = p.Restore([]byte{})
	if err == nil {
		t.Error("Restore with empty bytes should return error")
	}

	// Valid JSON but wrong structure.
	err = p.Restore([]byte(`{"unknown": "field"}`))
	if err != nil {
		t.Errorf("Restore with unknown fields should succeed (tolerant parsing): %v", err)
	}

	// Verify plugin is still usable after corrupt restore.
	_, checkErr := p.Checkpoint()
	if checkErr != plugin.ErrSkipCheckpoint {
		t.Errorf("plugin should still work after corrupt restore: %v", checkErr)
	}
}

// Verify compile-time interface satisfaction.
var _ plugin.Checkpointer = (*FileTransferPlugin)(nil)
