package macaroon

import (
	"testing"
	"time"
)

func TestParseCaveat(t *testing.T) {
	tests := []struct {
		input   string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{"service=proxy", "service", "proxy", false},
		{"action=invite,connect", "action", "invite,connect", false},
		{"peers_max=5", "peers_max", "5", false},
		{"expires=2026-12-31T00:00:00Z", "expires", "2026-12-31T00:00:00Z", false},
		{"delegate=true", "delegate", "true", false},
		{"network=/shurli/kad/1.0.0", "network", "/shurli/kad/1.0.0", false},
		{"group=family", "group", "family", false},
		{"no-equals-sign", "", "", true},
		{"=value-no-key", "", "", true},
	}

	for _, tt := range tests {
		k, v, err := ParseCaveat(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseCaveat(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCaveat(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if k != tt.wantKey || v != tt.wantVal {
			t.Errorf("ParseCaveat(%q) = (%q, %q), want (%q, %q)", tt.input, k, v, tt.wantKey, tt.wantVal)
		}
	}
}

func TestDefaultVerifierService(t *testing.T) {
	v := DefaultVerifier(VerifyContext{Service: "proxy"})
	if err := v("service=proxy,ping"); err != nil {
		t.Errorf("proxy should be in allowed list: %v", err)
	}
	if err := v("service=ping"); err == nil {
		t.Error("proxy not in 'ping' list, should fail")
	}
}

func TestDefaultVerifierAction(t *testing.T) {
	v := DefaultVerifier(VerifyContext{Action: "connect"})
	if err := v("action=invite,connect"); err != nil {
		t.Errorf("connect should be in allowed list: %v", err)
	}
	if err := v("action=admin"); err == nil {
		t.Error("connect not in 'admin' list, should fail")
	}
}

func TestDefaultVerifierPeersMax(t *testing.T) {
	v := DefaultVerifier(VerifyContext{PeersUsed: 3})
	if err := v("peers_max=5"); err != nil {
		t.Errorf("3 < 5, should pass: %v", err)
	}
	if err := v("peers_max=3"); err == nil {
		t.Error("3 >= 3, should fail")
	}
	if err := v("peers_max=2"); err == nil {
		t.Error("3 >= 2, should fail")
	}
}

func TestDefaultVerifierExpires(t *testing.T) {
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	v := DefaultVerifier(VerifyContext{Now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	if err := v("expires=" + future.Format(time.RFC3339)); err != nil {
		t.Errorf("future expiry should pass: %v", err)
	}
	if err := v("expires=" + past.Format(time.RFC3339)); err == nil {
		t.Error("past expiry should fail")
	}
}

func TestDefaultVerifierDelegate(t *testing.T) {
	v := DefaultVerifier(VerifyContext{IsDelegation: true})
	if err := v("delegate=true"); err != nil {
		t.Errorf("delegate=true should allow delegation: %v", err)
	}
	if err := v("delegate=false"); err == nil {
		t.Error("delegate=false should reject delegation")
	}
}

func TestDefaultVerifierGroup(t *testing.T) {
	v := DefaultVerifier(VerifyContext{Group: "family"})
	if err := v("group=family"); err != nil {
		t.Errorf("matching group should pass: %v", err)
	}
	if err := v("group=work"); err == nil {
		t.Error("mismatched group should fail")
	}
}

func TestDefaultVerifierNetwork(t *testing.T) {
	v := DefaultVerifier(VerifyContext{Network: "/shurli/kad/1.0.0"})
	if err := v("network=/shurli/kad/1.0.0"); err != nil {
		t.Errorf("matching network should pass: %v", err)
	}
	if err := v("network=/other/kad/1.0.0"); err == nil {
		t.Error("mismatched network should fail")
	}
}

func TestDefaultVerifierUnknownCaveat(t *testing.T) {
	v := DefaultVerifier(VerifyContext{})
	if err := v("unknown_key=value"); err == nil {
		t.Error("unknown caveat should be rejected (fail-closed)")
	}
}

func TestDefaultVerifierEmptyContext(t *testing.T) {
	// When context fields are empty, most caveats skip the check
	v := DefaultVerifier(VerifyContext{Now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	if err := v("service=proxy"); err != nil {
		t.Errorf("empty service context should skip: %v", err)
	}
	if err := v("action=admin"); err != nil {
		t.Errorf("empty action context should skip: %v", err)
	}
	if err := v("group=family"); err != nil {
		t.Errorf("empty group context should skip: %v", err)
	}
}
