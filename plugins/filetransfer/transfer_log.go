package filetransfer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TransferEvent is a structured log entry for a file transfer event.
type TransferEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Direction string    `json:"direction"` // "send" or "receive"
	PeerID    string    `json:"peer_id"`
	FileName  string    `json:"file_name"`
	FileSize  int64     `json:"file_size,omitempty"`
	BytesDone int64     `json:"bytes_done,omitempty"`
	Error     string    `json:"error,omitempty"`
	Duration  string    `json:"duration,omitempty"`
}

// Transfer event type constants.
const (
	EventLogRequestReceived = "request_received"
	EventLogAccepted        = "accepted"
	EventLogRejected        = "rejected"
	EventLogStarted         = "started"
	EventLogProgress25      = "progress_25"
	EventLogProgress50      = "progress_50"
	EventLogProgress75      = "progress_75"
	EventLogCompleted       = "completed"
	EventLogFailed          = "failed"
	EventLogResumed            = "resumed"
	EventLogCancelled          = "cancelled"
	EventLogSpamBlocked        = "spam_blocked"
	EventLogDiskSpaceRejected  = "disk_space_rejected"
	EventLogMultiPeerRejected  = "multi_peer_rejected" // IF16-5
	EventLogPathFailover       = "path_failover"       // TS-5b: automatic path failover (F10)
)

// TransferLogger writes structured JSON transfer events to a rotating log file.
type TransferLogger struct {
	path     string
	mu       sync.Mutex
	file     *os.File
	size     int64
	maxSize  int64 // bytes per file before rotation
	maxFiles int   // number of rotated files to keep
}

// NewTransferLogger creates a new logger that writes to the given path.
// Default rotation: 10 MB per file, 3 rotated files kept.
func NewTransferLogger(path string) (*TransferLogger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open transfer log: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat transfer log: %w", err)
	}

	return &TransferLogger{
		path:     path,
		file:     f,
		size:     info.Size(),
		maxSize:  10 * 1024 * 1024, // 10 MB
		maxFiles: 3,
	}, nil
}

// Log writes a transfer event as a JSON line.
func (l *TransferLogger) Log(event TransferEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	n, err := l.file.Write(data)
	if err != nil {
		return
	}
	l.size += int64(n)

	if l.size >= l.maxSize {
		l.rotate()
	}
}

// rotate moves the current log file to .1, .1 to .2, etc.
// Must be called with l.mu held.
func (l *TransferLogger) rotate() {
	l.file.Close()

	// Shift existing rotated files: .3 -> delete, .2 -> .3, .1 -> .2
	for i := l.maxFiles; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		if i == l.maxFiles {
			os.Remove(src)
			continue
		}
		dst := fmt.Sprintf("%s.%d", l.path, i+1)
		os.Rename(src, dst)
	}

	// Current -> .1
	os.Rename(l.path, l.path+".1")

	// Open fresh file.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	l.file = f
	l.size = 0
}

// Close closes the log file.
func (l *TransferLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// ReadEvents reads up to max events from the log file (newest first).
// Reads from the current log file only (not rotated files).
func ReadTransferEvents(path string, max int) ([]TransferEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []TransferEvent
	scanner := bufio.NewScanner(f)
	// Increase buffer size for potentially long JSON lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		var ev TransferEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}

	// Return newest first (reverse order), limited to max.
	result := make([]TransferEvent, 0, min(max, len(events)))
	for i := len(events) - 1; i >= 0 && len(result) < max; i-- {
		result = append(result, events[i])
	}
	return result, nil
}
