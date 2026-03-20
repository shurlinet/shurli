// DeliveryQueue holds pending grant deliveries for peers that were offline
// when the admin created or revoked a grant. The queue is stored on the
// granting node only. The relay never touches grant tokens.
package grants

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DefaultDeliveryQueueTTL = 7 * 24 * time.Hour // 7 days
	maxQueuedItems          = 100
)

// DeliveryItem is a single pending delivery.
type DeliveryItem struct {
	TargetIDStr string    `json:"target_id"`
	MsgType     byte      `json:"-"`          // wire message type (msgGrantDeliver or msgGrantRevoke)
	MsgTypeStr  string    `json:"msg_type"`   // human-readable: "deliver" or "revoke"
	Payload     []byte    `json:"payload"`    // JSON-encoded GrantDelivery or GrantRevocation
	QueuedAt    time.Time `json:"queued_at"`
	ExpiresAt   time.Time `json:"expires_at"` // when the queued item itself expires (not the grant)
}

func msgTypeToStr(b byte) string {
	switch b {
	case msgGrantDeliver:
		return "deliver"
	case msgGrantRevoke:
		return "revoke"
	default:
		return fmt.Sprintf("unknown_%d", b)
	}
}

func strToMsgType(s string) byte {
	switch s {
	case "deliver":
		return msgGrantDeliver
	case "revoke":
		return msgGrantRevoke
	default:
		return 0
	}
}

// deliveryQueueFile is the JSON structure written to disk.
type deliveryQueueFile struct {
	Version uint64         `json:"version"`
	Items   []DeliveryItem `json:"items"`
	HMAC    string         `json:"hmac,omitempty"`
}

// DeliveryQueue manages pending grant deliveries for offline peers.
type DeliveryQueue struct {
	mu          sync.Mutex
	items       []DeliveryItem
	hmacKey     []byte
	persistPath string
	version     uint64
	ttl         time.Duration // how long items stay in queue
	logger      *slog.Logger
}

// NewDeliveryQueue creates a new delivery queue.
func NewDeliveryQueue(hmacKey []byte, ttl time.Duration) *DeliveryQueue {
	if ttl <= 0 {
		ttl = DefaultDeliveryQueueTTL
	}
	return &DeliveryQueue{
		hmacKey: hmacKey,
		ttl:     ttl,
		logger:  slog.Default(),
	}
}

// SetPersistPath sets the file path for auto-saving the queue.
func (q *DeliveryQueue) SetPersistPath(path string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.persistPath = path
}

// Enqueue adds a delivery to the queue for the target peer.
// Returns an error if the queue is full.
func (q *DeliveryQueue) Enqueue(targetID peer.ID, msgType byte, payload []byte) error {
	q.mu.Lock()

	// Clean expired items first.
	q.cleanExpiredLocked()

	if len(q.items) >= maxQueuedItems {
		q.mu.Unlock()
		return fmt.Errorf("delivery queue full (%d items)", maxQueuedItems)
	}

	item := DeliveryItem{
		TargetIDStr: targetID.String(),
		MsgType:     msgType,
		MsgTypeStr:  msgTypeToStr(msgType),
		Payload:     payload,
		QueuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(q.ttl),
	}
	q.items = append(q.items, item)
	q.mu.Unlock()

	if err := q.save(); err != nil {
		q.logger.Error("delivery-queue: failed to persist after enqueue", "target", shortPeerID(targetID), "error", err)
	}

	q.logger.Info("delivery-queue: enqueued", "target", shortPeerID(targetID), "type", msgType, "ttl", q.ttl)
	return nil
}

// Dequeue returns and removes all pending deliveries for the given peer.
// Items that have expired or whose grant tokens have expired are filtered out.
func (q *DeliveryQueue) Dequeue(targetID peer.ID) []DeliveryItem {
	targetStr := targetID.String()
	now := time.Now()

	q.mu.Lock()

	var matched []DeliveryItem
	var remaining []DeliveryItem

	for _, item := range q.items {
		if now.After(item.ExpiresAt) {
			// Queue item expired, skip.
			continue
		}
		if item.TargetIDStr == targetStr {
			// Check if the grant itself has already expired.
			if item.MsgType == msgGrantDeliver && grantAlreadyExpired(item.Payload) {
				q.logger.Info("delivery-queue: skipping expired grant", "target", shortPeerID(targetID))
				continue
			}
			matched = append(matched, item)
		} else {
			remaining = append(remaining, item)
		}
	}

	q.items = remaining
	q.mu.Unlock()

	if len(matched) > 0 {
		if err := q.save(); err != nil {
			q.logger.Error("delivery-queue: failed to persist after dequeue", "target", shortPeerID(targetID), "error", err)
		}
		q.logger.Info("delivery-queue: dequeued", "target", shortPeerID(targetID), "count", len(matched))
	}

	return matched
}

// Len returns the number of items in the queue.
func (q *DeliveryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// cleanExpiredLocked removes expired items. Must be called with q.mu held.
func (q *DeliveryQueue) cleanExpiredLocked() {
	now := time.Now()
	var kept []DeliveryItem
	for _, item := range q.items {
		if now.Before(item.ExpiresAt) {
			kept = append(kept, item)
		}
	}
	q.items = kept
}

// grantAlreadyExpired checks if a delivery payload contains an already-expired grant.
func grantAlreadyExpired(payload []byte) bool {
	var delivery GrantDelivery
	if err := json.Unmarshal(payload, &delivery); err != nil {
		return false
	}
	if delivery.Permanent {
		return false
	}
	if delivery.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, delivery.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(t)
}

// save persists the queue to disk with HMAC integrity.
func (q *DeliveryQueue) save() error {
	q.mu.Lock()
	path := q.persistPath
	if path == "" {
		q.mu.Unlock()
		return nil
	}

	q.version++
	version := q.version

	// Copy items for serialization.
	items := make([]DeliveryItem, len(q.items))
	copy(items, q.items)
	hmacKey := q.hmacKey
	q.mu.Unlock()

	qf := deliveryQueueFile{
		Version: version,
		Items:   items,
	}

	if len(hmacKey) > 0 {
		itemsJSON, _ := json.Marshal(items)
		qf.HMAC = computeFileHMAC(hmacKey, version, itemsJSON)
	}

	data, err := json.MarshalIndent(qf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal delivery queue: %w", err)
	}

	// A2 mitigation: reject symlinks.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("delivery queue file is a symlink, refusing to write")
		}
	}

	return atomicWriteFile(path, data, 0600)
}

// LoadDeliveryQueue reads the queue from disk and verifies HMAC integrity.
func LoadDeliveryQueue(path string, hmacKey []byte, ttl time.Duration) (*DeliveryQueue, error) {
	q := NewDeliveryQueue(hmacKey, ttl)
	q.SetPersistPath(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return q, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read delivery queue file: %w", err)
	}

	var file deliveryQueueFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse delivery queue file: %w", err)
	}

	// Verify HMAC integrity.
	itemsJSON, _ := json.Marshal(file.Items)
	if err := verifyFileHMAC(hmacKey, file.HMAC, file.Version, itemsJSON, len(file.Items) > 0); err != nil {
		return nil, fmt.Errorf("delivery queue file: %w", err)
	}

	q.version = file.Version

	// Load items, skipping expired ones.
	now := time.Now()
	for _, item := range file.Items {
		if now.After(item.ExpiresAt) {
			q.logger.Info("delivery-queue: skipping expired item on load", "target", truncateStr(item.TargetIDStr, 16))
			continue
		}
		// Restore MsgType from persisted string.
		item.MsgType = strToMsgType(item.MsgTypeStr)
		if item.MsgType == 0 {
			q.logger.Warn("delivery-queue: skipping item with unknown msg_type on load",
				"target", truncateStr(item.TargetIDStr, 16), "msg_type", item.MsgTypeStr)
			continue
		}
		q.items = append(q.items, item)
	}

	q.logger.Info("delivery-queue: loaded", "count", len(q.items), "version", q.version)
	return q, nil
}
