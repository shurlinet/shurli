package grants

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeliveryQueueEnqueueDequeue(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)

	pid := genPeerID(t)
	payload := []byte(`{"token":"abc","permanent":true}`)

	if err := q.Enqueue(pid, msgGrantDeliver, payload); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if q.Len() != 1 {
		t.Fatalf("expected 1 item, got %d", q.Len())
	}

	items := q.Dequeue(pid)
	if len(items) != 1 {
		t.Fatalf("expected 1 dequeued item, got %d", len(items))
	}
	if items[0].MsgType != msgGrantDeliver {
		t.Fatalf("wrong message type: %d", items[0].MsgType)
	}

	// Queue should be empty now.
	if q.Len() != 0 {
		t.Fatalf("expected 0 items after dequeue, got %d", q.Len())
	}
}

func TestDeliveryQueueMultiplePeers(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)

	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	q.Enqueue(pid1, msgGrantDeliver, []byte(`{"token":"a","permanent":true}`))
	q.Enqueue(pid2, msgGrantDeliver, []byte(`{"token":"b","permanent":true}`))
	q.Enqueue(pid1, msgGrantRevoke, []byte(`{"reason":"test"}`))

	// Dequeue pid1 should get 2 items.
	items := q.Dequeue(pid1)
	if len(items) != 2 {
		t.Fatalf("expected 2 items for pid1, got %d", len(items))
	}

	// pid2 should still have 1.
	if q.Len() != 1 {
		t.Fatalf("expected 1 remaining, got %d", q.Len())
	}

	items2 := q.Dequeue(pid2)
	if len(items2) != 1 {
		t.Fatalf("expected 1 item for pid2, got %d", len(items2))
	}
}

func TestDeliveryQueueExpiry(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 1*time.Millisecond) // very short TTL

	pid := genPeerID(t)
	q.Enqueue(pid, msgGrantDeliver, []byte(`{"token":"expired","permanent":true}`))

	time.Sleep(5 * time.Millisecond)

	items := q.Dequeue(pid)
	if len(items) != 0 {
		t.Fatalf("expired items should not be returned, got %d", len(items))
	}
}

func TestDeliveryQueueSkipsExpiredGrant(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour) // long queue TTL

	pid := genPeerID(t)

	// Create a delivery payload with an already-expired grant.
	delivery := GrantDelivery{
		Token:     "dummytoken",
		ExpiresAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339), // expired 1h ago
	}
	payload, _ := json.Marshal(delivery)

	q.Enqueue(pid, msgGrantDeliver, payload)

	// The queue item is NOT expired (7d TTL), but the grant inside is.
	items := q.Dequeue(pid)
	if len(items) != 0 {
		t.Fatal("should skip delivery for already-expired grant")
	}
}

func TestDeliveryQueueMaxItems(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)

	pid := genPeerID(t)
	for i := 0; i < maxQueuedItems; i++ {
		if err := q.Enqueue(pid, msgGrantDeliver, []byte(`{"token":"x","permanent":true}`)); err != nil {
			t.Fatalf("enqueue %d should succeed: %v", i, err)
		}
	}

	// One more should fail.
	err := q.Enqueue(pid, msgGrantDeliver, []byte(`{"token":"y","permanent":true}`))
	if err == nil {
		t.Fatal("should reject when queue is full")
	}
}

func TestDeliveryQueuePersistAndLoad(t *testing.T) {
	_, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_delivery_queue.json")

	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)
	q.SetPersistPath(path)

	pid := genPeerID(t)
	q.Enqueue(pid, msgGrantDeliver, []byte(`{"token":"persist-test","permanent":true}`))

	// Load into new queue.
	q2, err := LoadDeliveryQueue(path, hmacKey, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if q2.Len() != 1 {
		t.Fatalf("expected 1 item after load, got %d", q2.Len())
	}

	items := q2.Dequeue(pid)
	if len(items) != 1 {
		t.Fatalf("expected 1 dequeued item, got %d", len(items))
	}
}

func TestDeliveryQueueHMACTamperDetection(t *testing.T) {
	_, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_delivery_queue.json")

	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)
	q.SetPersistPath(path)

	pid := genPeerID(t)
	q.Enqueue(pid, msgGrantDeliver, []byte(`{"token":"tamper-test","permanent":true}`))

	// Tamper with file.
	data, _ := os.ReadFile(path)
	tampered := append(data[:len(data)-5], []byte("XXXXX")...)
	os.WriteFile(path, tampered, 0600)

	_, err := LoadDeliveryQueue(path, hmacKey, 7*24*time.Hour)
	if err == nil {
		t.Fatal("should detect tampered file")
	}
}

func TestDeliveryQueueLoadNonexistent(t *testing.T) {
	_, hmacKey := genKeys(t)
	q, err := LoadDeliveryQueue("/nonexistent/path/queue.json", hmacKey, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("load nonexistent: %v", err)
	}
	if q.Len() != 0 {
		t.Fatal("should have empty queue")
	}
}

func TestParseDurationExtended(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"3d12h", 3*24*time.Hour + 12*time.Hour, false},
		{"1w2d", 9 * 24 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseDurationExtended(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDurationExtended(%q) should error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDurationExtended(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("ParseDurationExtended(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestDeliveryQueueRevocationNotFilteredByGrantExpiry(t *testing.T) {
	_, hmacKey := genKeys(t)
	q := NewDeliveryQueue(hmacKey, 7*24*time.Hour)

	pid := genPeerID(t)

	// Revocation messages should never be filtered by grant expiry check.
	q.Enqueue(pid, msgGrantRevoke, []byte(`{"reason":"admin revoked"}`))

	items := q.Dequeue(pid)
	if len(items) != 1 {
		t.Fatal("revocation should not be filtered")
	}
}
