package plugins

import (
	"testing"

	"github.com/shurlinet/shurli/pkg/plugin"
)

// TestRegisterAllReturnsErrors verifies P17 fix:
// RegisterAll() now returns errors from Register() instead of discarding them.
func TestRegisterAllSilentlyDiscardsErrors(t *testing.T) {
	r := plugin.NewRegistry(&plugin.ContextProvider{})

	// First call succeeds.
	if err := RegisterAll(r); err != nil {
		t.Fatalf("first RegisterAll should succeed: %v", err)
	}

	// Second call should fail (duplicate "filetransfer") and return the error.
	err := RegisterAll(r)
	if err == nil {
		t.Fatal("P17 fix: second RegisterAll should return error for duplicate registration")
	}
	t.Logf("P17 fix verified: RegisterAll returned error: %v", err)

	plugins := r.List()
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin (duplicate rejected), got %d", len(plugins))
	}
}
