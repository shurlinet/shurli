package p2pnet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTransferLoggerWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transfers.log")

	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Write 3 events.
	now := time.Now()
	events := []TransferEvent{
		{Timestamp: now, EventType: EventLogStarted, Direction: "send", PeerID: "peer1", FileName: "file1.txt", FileSize: 1024},
		{Timestamp: now.Add(time.Second), EventType: EventLogProgress50, Direction: "send", PeerID: "peer1", FileName: "file1.txt", FileSize: 1024, BytesDone: 512},
		{Timestamp: now.Add(2 * time.Second), EventType: EventLogCompleted, Direction: "send", PeerID: "peer1", FileName: "file1.txt", FileSize: 1024, BytesDone: 1024, Duration: "1.5s"},
	}

	for _, ev := range events {
		logger.Log(ev)
	}

	// Read back (newest first).
	result, err := ReadTransferEvents(logPath, 50)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result))
	}

	// Newest first.
	if result[0].EventType != EventLogCompleted {
		t.Errorf("expected first event to be completed, got %s", result[0].EventType)
	}
	if result[2].EventType != EventLogStarted {
		t.Errorf("expected last event to be started, got %s", result[2].EventType)
	}

	// Check fields.
	if result[0].FileName != "file1.txt" {
		t.Errorf("expected file1.txt, got %s", result[0].FileName)
	}
	if result[0].Duration != "1.5s" {
		t.Errorf("expected duration 1.5s, got %s", result[0].Duration)
	}
}

func TestTransferLoggerMaxLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transfers.log")

	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Write 10 events.
	for i := 0; i < 10; i++ {
		logger.Log(TransferEvent{
			EventType: EventLogCompleted,
			Direction: "send",
			PeerID:    "peer1",
			FileName:  "file.txt",
			FileSize:  int64(i * 100),
		})
	}

	// Read with limit 3.
	result, err := ReadTransferEvents(logPath, 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result))
	}

	// Should be newest 3 (FileSize 900, 800, 700).
	if result[0].FileSize != 900 {
		t.Errorf("expected FileSize 900, got %d", result[0].FileSize)
	}
}

func TestTransferLoggerRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transfers.log")

	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Set small max size to trigger rotation quickly.
	logger.maxSize = 500

	// Write events until rotation happens.
	for i := 0; i < 50; i++ {
		logger.Log(TransferEvent{
			EventType: EventLogCompleted,
			Direction: "send",
			PeerID:    "peer1",
			FileName:  "file.txt",
			FileSize:  int64(i),
		})
	}

	// Check that rotated file exists.
	if _, err := os.Stat(logPath + ".1"); os.IsNotExist(err) {
		t.Error("expected rotated file .1 to exist")
	}

	// Current log should be smaller than max since it was just rotated.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 500 {
		t.Errorf("current log should be under 500 bytes after rotation, got %d", info.Size())
	}
}

func TestTransferLoggerRotationMaxFiles(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transfers.log")

	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Set tiny size and max 2 files to trigger multiple rotations.
	logger.maxSize = 200
	logger.maxFiles = 2

	// Write enough to trigger multiple rotations.
	for i := 0; i < 100; i++ {
		logger.Log(TransferEvent{
			EventType: EventLogCompleted,
			Direction: "send",
			PeerID:    "peer1",
			FileName:  "file.txt",
			FileSize:  int64(i),
		})
	}

	// .1 and .2 should exist, .3 should not (maxFiles=2).
	if _, err := os.Stat(logPath + ".1"); os.IsNotExist(err) {
		t.Error("expected .1 to exist")
	}
	if _, err := os.Stat(logPath + ".2"); os.IsNotExist(err) {
		t.Error("expected .2 to exist")
	}
	if _, err := os.Stat(logPath + ".3"); !os.IsNotExist(err) {
		t.Error("expected .3 to NOT exist with maxFiles=2")
	}
}

func TestTransferEventJSONSerialization(t *testing.T) {
	ev := TransferEvent{
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		EventType: EventLogCompleted,
		Direction: "receive",
		PeerID:    "12D3KooWTestPeer",
		FileName:  "photo.jpg",
		FileSize:  5242880,
		BytesDone: 5242880,
		Duration:  "2.3s",
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	s := string(data)
	if !strings.Contains(s, `"event_type":"completed"`) {
		t.Error("missing event_type in JSON")
	}
	if !strings.Contains(s, `"file_name":"photo.jpg"`) {
		t.Error("missing file_name in JSON")
	}

	// Round-trip.
	var ev2 TransferEvent
	if err := json.Unmarshal(data, &ev2); err != nil {
		t.Fatal(err)
	}
	if ev2.EventType != ev.EventType {
		t.Errorf("round-trip mismatch: %s != %s", ev2.EventType, ev.EventType)
	}
	if ev2.FileSize != ev.FileSize {
		t.Errorf("round-trip size mismatch: %d != %d", ev2.FileSize, ev.FileSize)
	}
}

func TestReadTransferEventsNonexistent(t *testing.T) {
	events, err := ReadTransferEvents("/nonexistent/path/transfers.log", 50)
	if err != nil {
		t.Fatal("expected nil error for nonexistent file")
	}
	if events != nil {
		t.Errorf("expected nil events, got %d", len(events))
	}
}

func TestTransferLoggerAutoTimestamp(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "transfers.log")

	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Log with zero timestamp - should auto-fill.
	logger.Log(TransferEvent{
		EventType: EventLogStarted,
		Direction: "send",
		PeerID:    "peer1",
		FileName:  "test.txt",
	})

	events, err := ReadTransferEvents(logPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("expected 1 event")
	}
	if events[0].Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}
