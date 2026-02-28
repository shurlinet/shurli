package relay

import (
	"bytes"
	"testing"
	"time"
)

func TestEncodeUnsealRequest(t *testing.T) {
	req := EncodeUnsealRequest("my-passphrase", "123456")

	// Verify wire format: [1 version] [2 BE pass-len] [N pass] [1 TOTP-len] [M TOTP]
	if req[0] != unsealWireVersion {
		t.Errorf("version = %d, want %d", req[0], unsealWireVersion)
	}

	passLen := int(req[1])<<8 | int(req[2])
	if passLen != 13 {
		t.Errorf("passphrase length = %d, want 13", passLen)
	}

	pass := string(req[3 : 3+passLen])
	if pass != "my-passphrase" {
		t.Errorf("passphrase = %q, want %q", pass, "my-passphrase")
	}

	totpLen := int(req[3+passLen])
	if totpLen != 6 {
		t.Errorf("TOTP length = %d, want 6", totpLen)
	}

	totpCode := string(req[4+passLen : 4+passLen+totpLen])
	if totpCode != "123456" {
		t.Errorf("TOTP code = %q, want %q", totpCode, "123456")
	}
}

func TestEncodeUnsealRequestNoTOTP(t *testing.T) {
	req := EncodeUnsealRequest("passphrase", "")

	passLen := int(req[1])<<8 | int(req[2])
	totpLen := int(req[3+passLen])
	if totpLen != 0 {
		t.Errorf("TOTP length = %d, want 0", totpLen)
	}

	// Total length: 1 version + 2 pass-len + 10 pass + 1 TOTP-len = 14
	if len(req) != 14 {
		t.Errorf("request length = %d, want 14", len(req))
	}
}

func TestReadUnsealResponseOK(t *testing.T) {
	msg := "unsealed"
	buf := make([]byte, 2+len(msg))
	buf[0] = unsealStatusOK
	buf[1] = byte(len(msg))
	copy(buf[2:], msg)

	ok, respMsg, err := ReadUnsealResponse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ReadUnsealResponse: %v", err)
	}
	if !ok {
		t.Error("expected OK status")
	}
	if respMsg != "unsealed" {
		t.Errorf("message = %q, want %q", respMsg, "unsealed")
	}
}

func TestReadUnsealResponseError(t *testing.T) {
	msg := "invalid passphrase"
	buf := make([]byte, 2+len(msg))
	buf[0] = unsealStatusErr
	buf[1] = byte(len(msg))
	copy(buf[2:], msg)

	ok, respMsg, err := ReadUnsealResponse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ReadUnsealResponse: %v", err)
	}
	if ok {
		t.Error("expected error status")
	}
	if respMsg != "invalid passphrase" {
		t.Errorf("message = %q, want %q", respMsg, "invalid passphrase")
	}
}

func TestReadUnsealResponseEmptyMessage(t *testing.T) {
	buf := []byte{unsealStatusOK, 0}
	ok, msg, err := ReadUnsealResponse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ReadUnsealResponse: %v", err)
	}
	if !ok {
		t.Error("expected OK status")
	}
	if msg != "" {
		t.Errorf("message = %q, want empty", msg)
	}
}

func TestUnsealLockout(t *testing.T) {
	handler := NewUnsealHandler(nil, "")
	pid := genPeerID(t)

	// First unsealFreeAttempts failures: no lockout (typo grace period).
	for i := 0; i < unsealFreeAttempts; i++ {
		locked, _ := handler.isLockedOut(pid)
		if locked {
			t.Fatalf("attempt %d should not be locked out", i+1)
		}
		handler.recordFailure(pid)
	}

	// Still not locked after free attempts exhausted (lockout starts on NEXT failure).
	locked, _ := handler.isLockedOut(pid)
	if locked {
		t.Fatal("should not be locked after free attempts (lockout triggers on next failure)")
	}

	// Next failure triggers lockout.
	handler.recordFailure(pid)
	locked, remaining := handler.isLockedOut(pid)
	if !locked {
		t.Fatal("should be locked after exceeding free attempts")
	}
	if remaining <= 0 {
		t.Fatalf("remaining = %v, want > 0", remaining)
	}
	// First lockout should be ~1 minute.
	if remaining > 2*time.Minute {
		t.Fatalf("first lockout remaining = %v, want <= 2m", remaining)
	}
}

func TestUnsealLockoutEscalates(t *testing.T) {
	handler := NewUnsealHandler(nil, "")
	pid := genPeerID(t)

	// Burn through free attempts.
	for i := 0; i < unsealFreeAttempts; i++ {
		handler.recordFailure(pid)
	}

	// Each subsequent failure should escalate the lockout duration.
	var prevDuration time.Duration
	for i, expected := range unsealLockoutSchedule {
		handler.recordFailure(pid)
		_, remaining := handler.isLockedOut(pid)

		// Remaining should be close to the expected duration (within 1s of recording).
		if remaining < expected-time.Second || remaining > expected+time.Second {
			t.Errorf("failure %d: remaining=%v, want ~%v", unsealFreeAttempts+i+1, remaining, expected)
		}

		if i > 0 && remaining <= prevDuration-time.Second {
			t.Errorf("failure %d: lockout did not escalate (%v <= %v)", unsealFreeAttempts+i+1, remaining, prevDuration)
		}
		prevDuration = remaining

		// Clear lockout time so next recordFailure works immediately.
		handler.mu.Lock()
		handler.lockouts[pid].lockedUntil = time.Time{}
		handler.mu.Unlock()
	}
}

func TestUnsealLockoutResetOnSuccess(t *testing.T) {
	handler := NewUnsealHandler(nil, "")
	pid := genPeerID(t)

	// Accumulate failures past the free limit.
	for i := 0; i < unsealFreeAttempts+2; i++ {
		handler.recordFailure(pid)
	}

	locked, _ := handler.isLockedOut(pid)
	if !locked {
		t.Fatal("should be locked")
	}

	// Successful unseal resets everything.
	handler.resetLockout(pid)

	locked, _ = handler.isLockedOut(pid)
	if locked {
		t.Fatal("should not be locked after reset")
	}

	if handler.getFailures(pid) != 0 {
		t.Fatalf("failures = %d, want 0 after reset", handler.getFailures(pid))
	}
}

func TestUnsealLockoutPermanentBlock(t *testing.T) {
	handler := NewUnsealHandler(nil, "")
	pid := genPeerID(t)

	// Exhaust all free attempts + all lockout schedule entries.
	totalBeforeBlock := unsealFreeAttempts + len(unsealLockoutSchedule)
	for i := 0; i < totalBeforeBlock; i++ {
		handler.recordFailure(pid)
		// Clear lockout timers so we can keep failing immediately.
		handler.mu.Lock()
		handler.lockouts[pid].lockedUntil = time.Time{}
		handler.mu.Unlock()
	}

	// Not yet blocked (last schedule entry was just used).
	handler.mu.Lock()
	blocked := handler.lockouts[pid].blocked
	handler.mu.Unlock()
	if blocked {
		t.Fatal("should not be blocked yet (last schedule entry just used)")
	}

	// One more failure triggers permanent block.
	handler.recordFailure(pid)

	locked, remaining := handler.isLockedOut(pid)
	if !locked {
		t.Fatal("should be locked")
	}
	if remaining != -1 {
		t.Fatalf("remaining = %v, want -1 (permanent block)", remaining)
	}

	// Reset still works (admin can SSH and clear it).
	handler.resetLockout(pid)
	locked, _ = handler.isLockedOut(pid)
	if locked {
		t.Fatal("should not be locked after admin reset")
	}
}

func TestUnsealFailureMessageBlock(t *testing.T) {
	handler := NewUnsealHandler(nil, "")

	// Past the schedule: shows permanent block message.
	msg := handler.failureMessage("invalid passphrase", unsealFreeAttempts+len(unsealLockoutSchedule)+1)
	if msg != "invalid passphrase (permanently blocked: unseal via SSH on the relay server)" {
		t.Errorf("unexpected block message: %s", msg)
	}
}

func TestUnsealFailureMessage(t *testing.T) {
	handler := NewUnsealHandler(nil, "")

	// Within free attempts: shows remaining.
	msg := handler.failureMessage("invalid passphrase", 2)
	if msg != "invalid passphrase (2 attempts remaining before lockout)" {
		t.Errorf("unexpected message: %s", msg)
	}

	// Last free attempt.
	msg = handler.failureMessage("invalid passphrase", unsealFreeAttempts-1)
	if msg != "invalid passphrase (1 attempt remaining before lockout)" {
		t.Errorf("unexpected message: %s", msg)
	}

	// Past free attempts: shows lockout duration.
	msg = handler.failureMessage("invalid passphrase", unsealFreeAttempts+1)
	if msg != "invalid passphrase (locked for 1 minute)" {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// Encode a request
	req := EncodeUnsealRequest("test-pass", "654321")

	// Verify we can parse it back by reading the wire format manually
	if req[0] != unsealWireVersion {
		t.Fatal("wrong version")
	}

	passLen := int(req[1])<<8 | int(req[2])
	pass := string(req[3 : 3+passLen])
	totpLen := int(req[3+passLen])
	totp := string(req[4+passLen : 4+passLen+totpLen])

	if pass != "test-pass" {
		t.Errorf("passphrase = %q, want %q", pass, "test-pass")
	}
	if totp != "654321" {
		t.Errorf("TOTP = %q, want %q", totp, "654321")
	}
}
