package p2pnet

import (
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func genTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}
	return pid
}

func TestNewNameResolver(t *testing.T) {
	r := NewNameResolver()
	if r == nil {
		t.Fatal("NewNameResolver returned nil")
	}
	if len(r.List()) != 0 {
		t.Error("new resolver should have empty list")
	}
}

func TestNameResolverRegister(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)

	// Valid registration
	if err := r.Register("home", pid); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Empty name should fail
	if err := r.Register("", pid); err == nil {
		t.Error("expected error for empty name")
	}

	// Overwrite with same name (allowed)
	pid2 := genTestPeerID(t)
	if err := r.Register("home", pid2); err != nil {
		t.Fatalf("Register overwrite: %v", err)
	}
	resolved, err := r.Resolve("home")
	if err != nil {
		t.Fatalf("Resolve after overwrite: %v", err)
	}
	if resolved != pid2 {
		t.Error("overwritten name should resolve to new peer ID")
	}
}

func TestNameResolverUnregister(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)

	r.Register("home", pid)
	r.Unregister("home")

	_, err := r.Resolve("home")
	if err == nil {
		t.Error("expected error after unregister")
	}

	// Unregister non-existent name (no-op, no error)
	r.Unregister("nonexistent")
}

func TestNameResolverResolve(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)
	r.Register("home", pid)

	t.Run("by name", func(t *testing.T) {
		resolved, err := r.Resolve("home")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved != pid {
			t.Errorf("got %s, want %s", resolved, pid)
		}
	})

	t.Run("by peer ID string", func(t *testing.T) {
		resolved, err := r.Resolve(pid.String())
		if err != nil {
			t.Fatalf("Resolve by peer ID: %v", err)
		}
		if resolved != pid {
			t.Errorf("got %s, want %s", resolved, pid)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := r.Resolve("nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent name")
		}
		if !errors.Is(err, ErrNameNotFound) {
			t.Errorf("expected ErrNameNotFound, got: %v", err)
		}
	})
}

func TestNameResolverList(t *testing.T) {
	r := NewNameResolver()
	pid1 := genTestPeerID(t)
	pid2 := genTestPeerID(t)

	r.Register("home", pid1)
	r.Register("work", pid2)

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(list))
	}
	if list["home"] != pid1 {
		t.Errorf("home = %s, want %s", list["home"], pid1)
	}
	if list["work"] != pid2 {
		t.Errorf("work = %s, want %s", list["work"], pid2)
	}

	// Verify returned map is a copy (modifying it doesn't affect resolver)
	list["home"] = pid2
	list2 := r.List()
	if list2["home"] != pid1 {
		t.Error("List() should return a copy")
	}
}

func TestNameResolverLoadFromMap(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)

	t.Run("valid", func(t *testing.T) {
		names := map[string]string{
			"home": pid.String(),
		}
		if err := r.LoadFromMap(names); err != nil {
			t.Fatalf("LoadFromMap: %v", err)
		}
		resolved, err := r.Resolve("home")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved != pid {
			t.Errorf("got %s, want %s", resolved, pid)
		}
	})

	t.Run("invalid peer ID", func(t *testing.T) {
		names := map[string]string{
			"bad": "not-a-valid-peer-id",
		}
		if err := r.LoadFromMap(names); err == nil {
			t.Error("expected error for invalid peer ID")
		}
	})

	t.Run("empty map", func(t *testing.T) {
		if err := r.LoadFromMap(map[string]string{}); err != nil {
			t.Fatalf("LoadFromMap empty: %v", err)
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		r2 := NewNameResolver()
		pid2 := genTestPeerID(t)
		names := map[string]string{
			"  spaced  ": pid2.String(),
		}
		if err := r2.LoadFromMap(names); err != nil {
			t.Fatalf("LoadFromMap: %v", err)
		}
		// Should resolve without spaces.
		resolved, err := r2.Resolve("spaced")
		if err != nil {
			t.Fatalf("Resolve trimmed name: %v", err)
		}
		if resolved != pid2 {
			t.Errorf("got %s, want %s", resolved, pid2)
		}
	})

	t.Run("empty name after trim skipped", func(t *testing.T) {
		r2 := NewNameResolver()
		pid2 := genTestPeerID(t)
		names := map[string]string{
			"   ":    pid2.String(),
			"valid":  pid2.String(),
		}
		if err := r2.LoadFromMap(names); err != nil {
			t.Fatalf("LoadFromMap: %v", err)
		}
		list := r2.List()
		if len(list) != 1 {
			t.Errorf("expected 1 entry (whitespace-only skipped), got %d", len(list))
		}
	})
}

func TestNameResolverCaseInsensitive(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)
	r.Register("home-node", pid)

	tests := []string{"home-node", "HOME-NODE", "Home-Node", "HOME-node", "  Home-Node  "}
	for _, name := range tests {
		resolved, err := r.Resolve(name)
		if err != nil {
			t.Errorf("Resolve(%q): %v", name, err)
			continue
		}
		if resolved != pid {
			t.Errorf("Resolve(%q) = %s, want %s", name, resolved, pid)
		}
	}
}

func TestNameResolverCaseDuplicatePrevention(t *testing.T) {
	r := NewNameResolver()
	pid1 := genTestPeerID(t)
	pid2 := genTestPeerID(t)

	// Register same name with different cases - should overwrite, not duplicate.
	r.Register("Home", pid1)
	r.Register("home", pid2)

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 entry (no duplicates), got %d: %v", len(list), list)
	}

	// Should resolve to the latest registration.
	resolved, err := r.Resolve("HOME")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != pid2 {
		t.Error("expected latest registration to win")
	}

	// Unregister with different case should work.
	r.Unregister("HOME")
	_, err = r.Resolve("home")
	if err == nil {
		t.Error("expected error after unregister")
	}
	if len(r.List()) != 0 {
		t.Error("expected empty list after unregister")
	}
}

func TestLoadFromMapCaseNormalization(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)

	// Config with mixed case keys - should normalize.
	names := map[string]string{
		"Home-Node": pid.String(),
	}
	if err := r.LoadFromMap(names); err != nil {
		t.Fatalf("LoadFromMap: %v", err)
	}

	// Should be stored lowercase.
	list := r.List()
	if _, ok := list["home-node"]; !ok {
		t.Errorf("expected lowercase key 'home-node', got keys: %v", list)
	}
	if _, ok := list["Home-Node"]; ok {
		t.Error("should not have mixed-case key in list")
	}
}

func TestNameResolverWhitespace(t *testing.T) {
	r := NewNameResolver()
	pid := genTestPeerID(t)

	// Register with spaces (trimmed on store).
	r.Register("  my-peer  ", pid)

	// Resolve without spaces.
	resolved, err := r.Resolve("my-peer")
	if err != nil {
		t.Fatalf("Resolve trimmed: %v", err)
	}
	if resolved != pid {
		t.Errorf("got %s, want %s", resolved, pid)
	}

	// Resolve with spaces (trimmed on resolve).
	resolved, err = r.Resolve("  my-peer  ")
	if err != nil {
		t.Fatalf("Resolve with spaces: %v", err)
	}
	if resolved != pid {
		t.Errorf("got %s, want %s", resolved, pid)
	}

	// Unregister with spaces (trimmed + case-insensitive).
	r.Unregister("  MY-PEER  ")
	_, err = r.Resolve("my-peer")
	if err == nil {
		t.Error("expected error after unregister with spaces+case")
	}

	// Register only whitespace (should fail).
	if err := r.Register("   ", pid); err == nil {
		t.Error("expected error for whitespace-only name")
	}
}
