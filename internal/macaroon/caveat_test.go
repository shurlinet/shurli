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
		{"peer_id=12D3KooWExample1", "peer_id", "12D3KooWExample1", false},
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

func TestDefaultVerifierPeerID(t *testing.T) {
	v := DefaultVerifier(VerifyContext{PeerID: "12D3KooWExample1"})
	if err := v("peer_id=12D3KooWExample1"); err != nil {
		t.Errorf("matching peer ID should pass: %v", err)
	}
	if err := v("peer_id=12D3KooWExample2"); err == nil {
		t.Error("mismatched peer ID should fail")
	}
}

func TestDefaultVerifierPeerIDEmptyContext(t *testing.T) {
	v := DefaultVerifier(VerifyContext{})
	if err := v("peer_id=12D3KooWExample1"); err != nil {
		t.Errorf("empty peer ID context should skip: %v", err)
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

func TestDefaultVerifierDelegateTo(t *testing.T) {
	// delegate_to caveats are audit trail, not enforcement. Always pass.
	// Bearer enforcement is via peer_id + DelegateTo in VerifyContext.
	v := DefaultVerifier(VerifyContext{PeerID: "peerC"})
	if err := v("delegate_to=peerC"); err != nil {
		t.Errorf("delegate_to should always pass (audit trail): %v", err)
	}
	// Even non-matching delegate_to passes - it's a historical delegation step.
	if err := v("delegate_to=peerD"); err != nil {
		t.Errorf("delegate_to should always pass (multi-hop audit): %v", err)
	}
	v2 := DefaultVerifier(VerifyContext{})
	if err := v2("delegate_to=peerC"); err != nil {
		t.Errorf("delegate_to should always pass with empty context: %v", err)
	}
}

func TestDefaultVerifierPeerIDWithDelegateTo(t *testing.T) {
	// peer_id=B, delegate_to=C, presenting peer=C: peer_id should pass via delegation bypass
	v := DefaultVerifier(VerifyContext{PeerID: "peerC", DelegateTo: "peerC"})
	if err := v("peer_id=peerB"); err != nil {
		t.Errorf("peer_id should pass when DelegateTo matches PeerID: %v", err)
	}
	// peer_id=B, DelegateTo=D, presenting peer=C: should fail (neither match)
	v2 := DefaultVerifier(VerifyContext{PeerID: "peerC", DelegateTo: "peerD"})
	if err := v2("peer_id=peerB"); err == nil {
		t.Error("peer_id should fail when neither peer_id nor DelegateTo matches")
	}
}

func TestDefaultVerifierMaxDelegations(t *testing.T) {
	// Not a delegation attempt: max_delegations=0 should pass
	v := DefaultVerifier(VerifyContext{IsDelegation: false})
	if err := v("max_delegations=0"); err != nil {
		t.Errorf("max_delegations=0 without delegation should pass: %v", err)
	}
	// Delegation attempt: max_delegations=0 should fail
	v2 := DefaultVerifier(VerifyContext{IsDelegation: true})
	if err := v2("max_delegations=0"); err == nil {
		t.Error("max_delegations=0 with delegation should fail")
	}
	// Delegation attempt: max_delegations=3 should pass
	if err := v2("max_delegations=3"); err != nil {
		t.Errorf("max_delegations=3 with delegation should pass: %v", err)
	}
	// Unlimited: max_delegations=-1 should always pass
	if err := v2("max_delegations=-1"); err != nil {
		t.Errorf("max_delegations=-1 should always pass: %v", err)
	}
}

func TestExtractDelegateTo(t *testing.T) {
	// Single delegate_to
	caveats := []string{"peer_id=peerA", "delegate_to=peerB", "expires=2030-01-01T00:00:00Z"}
	got := ExtractDelegateTo(caveats)
	if got != "peerB" {
		t.Errorf("ExtractDelegateTo = %q, want %q", got, "peerB")
	}
	// Multi-hop chain: must return the LAST delegate_to (current bearer)
	chain := []string{"peer_id=peerA", "delegate_to=peerB", "delegate_to=peerC", "delegate_to=peerD"}
	got2 := ExtractDelegateTo(chain)
	if got2 != "peerD" {
		t.Errorf("ExtractDelegateTo multi-hop = %q, want %q", got2, "peerD")
	}
	// No delegate_to caveat
	got3 := ExtractDelegateTo([]string{"peer_id=peerA"})
	if got3 != "" {
		t.Errorf("ExtractDelegateTo without delegate_to = %q, want empty", got3)
	}
}

func TestDelegateToInjectionBlocked(t *testing.T) {
	// Security regression: a holder with max_delegations=0 manually injects
	// delegate_to=E and hands the token to E. HasPermissiveDelegation must
	// return false, which the TokenVerifier uses to reject the token.
	token := New("test-node", make([]byte, 32), "grant-test")
	token.AddFirstPartyCaveat("peer_id=peerB")
	token.AddFirstPartyCaveat("max_delegations=0")

	// B injects delegate_to=E (valid HMAC because B holds the token).
	token.AddFirstPartyCaveat("delegate_to=peerE")

	delegateTo := ExtractDelegateTo(token.Caveats)
	if delegateTo != "peerE" {
		t.Fatalf("ExtractDelegateTo = %q, want peerE", delegateTo)
	}

	// The TokenVerifier defense: reject if delegateTo is set but no permissive delegation.
	if HasPermissiveDelegation(token.Caveats) {
		t.Fatal("token with only max_delegations=0 must NOT have permissive delegation")
	}
}

func TestDelegateToInjectionAllowedForLegitChain(t *testing.T) {
	// A legitimate multi-hop token has max_delegations=3, then max_delegations=0.
	// HasPermissiveDelegation should return true (3 > 0 permits the chain).
	token := New("test-node", make([]byte, 32), "grant-test")
	token.AddFirstPartyCaveat("peer_id=peerB")
	token.AddFirstPartyCaveat("max_delegations=3")
	token.AddFirstPartyCaveat("delegate_to=peerC")
	token.AddFirstPartyCaveat("max_delegations=0") // C's hop: no further delegation

	if !HasPermissiveDelegation(token.Caveats) {
		t.Fatal("legitimate multi-hop token should have permissive delegation")
	}
}

func TestHasPermissiveDelegation(t *testing.T) {
	tests := []struct {
		caveats []string
		want    bool
	}{
		{[]string{"max_delegations=0"}, false},
		{[]string{"max_delegations=3"}, true},
		{[]string{"max_delegations=-1"}, true},
		{[]string{}, false},
		{[]string{"max_delegations=0", "max_delegations=3"}, true},
		{[]string{"peer_id=test"}, false},
	}
	for _, tt := range tests {
		got := HasPermissiveDelegation(tt.caveats)
		if got != tt.want {
			t.Errorf("HasPermissiveDelegation(%v) = %v, want %v", tt.caveats, got, tt.want)
		}
	}
}

func TestExtractEarliestExpires(t *testing.T) {
	// Single expires caveat.
	caveats := []string{"peer_id=peerA", "expires=2030-06-15T12:00:00Z"}
	got := ExtractEarliestExpires(caveats)
	want := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ExtractEarliestExpires single = %v, want %v", got, want)
	}

	// Multiple expires: should return the earliest.
	chain := []string{
		"expires=2030-12-31T00:00:00Z", // parent's expiry
		"expires=2030-06-15T00:00:00Z", // delegated shorter expiry
	}
	got2 := ExtractEarliestExpires(chain)
	want2 := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Errorf("ExtractEarliestExpires multi = %v, want %v", got2, want2)
	}

	// No expires caveat: returns zero time.
	got3 := ExtractEarliestExpires([]string{"peer_id=peerA"})
	if !got3.IsZero() {
		t.Errorf("ExtractEarliestExpires without expires = %v, want zero", got3)
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
