package relay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/grants"
)

func generateTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setupAuthKeys(t *testing.T, entries ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	content := ""
	for _, e := range entries {
		content += e + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestGrantStore(t *testing.T) *grants.Store {
	t.Helper()
	rootKey := []byte("test-root-key-32bytes-padding!!")
	hmacKey := []byte("test-hmac-key-32bytes-padding!!")
	gs := grants.NewStore(rootKey, hmacKey)
	gs.SetPersistPath(filepath.Join(t.TempDir(), "grants.json"))
	return gs
}

func TestCircuitACL_AllowReserve_AuthorizedPeer(t *testing.T) {
	p := generateTestPeerID(t)
	authPath := setupAuthKeys(t, p.String())
	acl := NewCircuitACL(authPath, false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow authorized peer")
	}
}

func TestCircuitACL_AllowReserve_UnauthorizedPeerDenied(t *testing.T) {
	authorized := generateTestPeerID(t)
	unauthorized := generateTestPeerID(t)
	authPath := setupAuthKeys(t, authorized.String())
	acl := NewCircuitACL(authPath, false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowReserve(unauthorized, addr) {
		t.Error("AllowReserve should deny unauthorized peer")
	}
}

func TestCircuitACL_AllowReserve_NoAuthPathAllowsAll(t *testing.T) {
	p := generateTestPeerID(t)
	acl := NewCircuitACL("", false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow all when no authKeysPath is set")
	}
}

func TestCircuitACL_EnableDataRelay_AllowsAll(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t, src.String(), dest.String())
	acl := NewCircuitACL(authPath, true, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowConnect(src, addr, dest) {
		t.Error("AllowConnect should return true when enableDataRelay is true")
	}
}

func TestCircuitACL_Disabled_AdminAllowed(t *testing.T) {
	admin := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		admin.String()+"  role=admin",
		member.String(),
	)
	acl := NewCircuitACL(authPath, false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")

	// Admin as source
	if !acl.AllowConnect(admin, addr, member) {
		t.Error("admin src should be allowed")
	}

	// Admin as destination
	if !acl.AllowConnect(member, addr, admin) {
		t.Error("admin dest should be allowed")
	}
}

func TestCircuitACL_Disabled_GrantedPeerAllowed(t *testing.T) {
	grantedPeer := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		grantedPeer.String(),
		member.String(),
	)

	gs := newTestGrantStore(t)
	gs.Grant(grantedPeer, 1*time.Hour, nil, false, 0)

	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")

	// Granted peer as source
	if !acl.AllowConnect(grantedPeer, addr, member) {
		t.Error("granted peer src should be allowed")
	}

	// Granted peer as destination
	if !acl.AllowConnect(member, addr, grantedPeer) {
		t.Error("granted peer dest should be allowed")
	}
}

func TestCircuitACL_Disabled_ExpiredGrantDenied(t *testing.T) {
	peerWithExpired := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		peerWithExpired.String(),
		member.String(),
	)

	gs := newTestGrantStore(t)
	// Grant with 1ms duration - will be expired by the time we check.
	gs.Grant(peerWithExpired, 1*time.Millisecond, nil, false, 0)
	time.Sleep(5 * time.Millisecond)

	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(peerWithExpired, addr, member) {
		t.Error("expired grant should be denied")
	}
}

func TestCircuitACL_Disabled_RegularMembersDenied(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t, src.String(), dest.String())
	gs := newTestGrantStore(t)
	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(src, addr, dest) {
		t.Error("regular members should be denied when data relay is disabled")
	}
}

func TestCircuitACL_Disabled_NoGrantStoreRegularMembersDenied(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t, src.String(), dest.String())
	acl := NewCircuitACL(authPath, false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(src, addr, dest) {
		t.Error("regular members should be denied when no grant store and data relay disabled")
	}
}

func TestCircuitACL_EmptyAuthKeys(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t) // empty file
	acl := NewCircuitACL(authPath, false, true, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(src, addr, dest) {
		t.Error("should deny when no peers in auth keys")
	}
}

func TestCircuitACL_GatingDisabled_AllowsAllReservations(t *testing.T) {
	p := generateTestPeerID(t)
	authPath := setupAuthKeys(t) // empty file
	acl := NewCircuitACL(authPath, false, false, nil)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow all when connection gating is disabled")
	}
}

func TestCircuitACL_GrantRevokeDeniesAccess(t *testing.T) {
	grantedPeer := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		grantedPeer.String(),
		member.String(),
	)

	gs := newTestGrantStore(t)
	gs.Grant(grantedPeer, 1*time.Hour, nil, false, 0)
	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")

	// Should be allowed with active grant.
	if !acl.AllowConnect(grantedPeer, addr, member) {
		t.Error("should be allowed with active grant")
	}

	// Revoke and check denied.
	gs.Revoke(grantedPeer)
	if acl.AllowConnect(grantedPeer, addr, member) {
		t.Error("should be denied after grant revocation")
	}
}

func TestCircuitACL_ServiceScopedGrantPassesRelayACL(t *testing.T) {
	// Regression test: service-scoped grants (e.g., --services file-transfer)
	// must still pass relay ACL. The relay checks with empty service ("") which
	// means "any grant qualifies", not a specific service match.
	grantedPeer := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		grantedPeer.String(),
		member.String(),
	)

	gs := newTestGrantStore(t)
	// Grant scoped to file-transfer only.
	gs.Grant(grantedPeer, 1*time.Hour, []string{"file-transfer"}, false, 0)

	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowConnect(grantedPeer, addr, member) {
		t.Error("service-scoped grant should still pass relay ACL (relay checks any grant, not specific service)")
	}
}

func TestCircuitACL_PermanentGrantAllowed(t *testing.T) {
	grantedPeer := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		grantedPeer.String(),
		member.String(),
	)

	gs := newTestGrantStore(t)
	gs.Grant(grantedPeer, 0, nil, true, 0) // permanent

	acl := NewCircuitACL(authPath, false, true, gs)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowConnect(grantedPeer, addr, member) {
		t.Error("permanent grant should be allowed")
	}
}
