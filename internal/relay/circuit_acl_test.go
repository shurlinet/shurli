package relay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
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

func TestCircuitACL_AllowReserve_AuthorizedPeer(t *testing.T) {
	p := generateTestPeerID(t)
	authPath := setupAuthKeys(t, p.String())
	acl := NewCircuitACL(authPath, false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow authorized peer")
	}
}

func TestCircuitACL_AllowReserve_UnauthorizedPeerDenied(t *testing.T) {
	authorized := generateTestPeerID(t)
	unauthorized := generateTestPeerID(t)
	authPath := setupAuthKeys(t, authorized.String())
	acl := NewCircuitACL(authPath, false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowReserve(unauthorized, addr) {
		t.Error("AllowReserve should deny unauthorized peer")
	}
}

func TestCircuitACL_AllowReserve_NoAuthPathAllowsAll(t *testing.T) {
	p := generateTestPeerID(t)
	acl := NewCircuitACL("", false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow all when no authKeysPath is set")
	}
}

func TestCircuitACL_EnableDataRelay_AllowsAll(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t, src.String(), dest.String())
	acl := NewCircuitACL(authPath, true, true)

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
	acl := NewCircuitACL(authPath, false, true)

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

func TestCircuitACL_Disabled_RelayDataAllowed(t *testing.T) {
	dataUser := generateTestPeerID(t)
	member := generateTestPeerID(t)
	authPath := setupAuthKeys(t,
		dataUser.String()+"  relay_data=true",
		member.String(),
	)
	acl := NewCircuitACL(authPath, false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")

	// relay_data peer as source
	if !acl.AllowConnect(dataUser, addr, member) {
		t.Error("relay_data src should be allowed")
	}

	// relay_data peer as destination
	if !acl.AllowConnect(member, addr, dataUser) {
		t.Error("relay_data dest should be allowed")
	}
}

func TestCircuitACL_Disabled_RegularMembersDenied(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t, src.String(), dest.String())
	acl := NewCircuitACL(authPath, false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(src, addr, dest) {
		t.Error("regular members should be denied when data relay is disabled")
	}
}

func TestCircuitACL_EmptyAuthKeys(t *testing.T) {
	src := generateTestPeerID(t)
	dest := generateTestPeerID(t)
	authPath := setupAuthKeys(t) // empty file
	acl := NewCircuitACL(authPath, false, true)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if acl.AllowConnect(src, addr, dest) {
		t.Error("should deny when no peers in auth keys")
	}
}

func TestCircuitACL_GatingDisabled_AllowsAllReservations(t *testing.T) {
	p := generateTestPeerID(t)
	authPath := setupAuthKeys(t) // empty file
	acl := NewCircuitACL(authPath, false, false)

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	if !acl.AllowReserve(p, addr) {
		t.Error("AllowReserve should allow all when connection gating is disabled")
	}
}

func TestHasRelayData(t *testing.T) {
	p := generateTestPeerID(t)
	pNoAttr := generateTestPeerID(t)

	authPath := setupAuthKeys(t,
		p.String()+"  relay_data=true  # data user",
		pNoAttr.String()+"  # regular member",
	)

	if !auth.HasRelayData(authPath, p) {
		t.Error("should detect relay_data=true")
	}
	if auth.HasRelayData(authPath, pNoAttr) {
		t.Error("should not detect relay_data on regular member")
	}
}
