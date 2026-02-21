package validate

import (
	"errors"
	"strings"
	"testing"
)

func TestNetworkName(t *testing.T) {
	valid := []string{
		"my-crew",
		"gaming-group",
		"a",
		"a1",
		"family",
		"org-internal",
		"x",
		"alpha-beta-gamma",
		"test123",
	}
	for _, name := range valid {
		if err := NetworkName(name); err != nil {
			t.Errorf("NetworkName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"My-Crew", "uppercase"},
		{"GAMING", "all uppercase"},
		{"my crew", "space"},
		{"-dash-start", "starts with hyphen"},
		{"dash-end-", "ends with hyphen"},
		{"-", "single hyphen"},
		{"has.dots", "dot"},
		{"has/slash", "slash"},
		{"has\\back", "backslash"},
		{"new\nline", "newline"},
		{"foo\tbar", "tab"},
		{"foo/../../etc", "path traversal"},
		{strings.Repeat("a", 64), "too long (64 chars)"},
		{"hello!", "exclamation"},
	}
	for _, tc := range invalid {
		if err := NetworkName(tc.name); err == nil {
			t.Errorf("NetworkName(%q) [%s] = nil, want error", tc.name, tc.desc)
		}
	}
}

func TestNetworkName_MaxLength(t *testing.T) {
	// 63 chars should be valid
	name63 := strings.Repeat("a", 63)
	if err := NetworkName(name63); err != nil {
		t.Errorf("NetworkName(63 chars) = %v, want nil", err)
	}

	// 64 chars should be invalid
	name64 := strings.Repeat("a", 64)
	if err := NetworkName(name64); err == nil {
		t.Error("NetworkName(64 chars) = nil, want error")
	}
}

func TestNetworkName_SentinelError(t *testing.T) {
	err := NetworkName("INVALID")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidNetworkName) {
		t.Errorf("error should wrap ErrInvalidNetworkName, got: %v", err)
	}
}
