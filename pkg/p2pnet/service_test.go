package p2pnet

import (
	"strings"
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	valid := []string{
		"ssh",
		"xrdp",
		"ollama",
		"my-service",
		"a",
		"a1",
		"x",
		"service-1",
		"my-long-service-name",
	}
	for _, name := range valid {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"SSH", "uppercase"},
		{"My-Service", "mixed case"},
		{"my service", "space"},
		{"foo/bar", "slash"},
		{"foo\\bar", "backslash"},
		{"foo\nbar", "newline"},
		{"foo\tbar", "tab"},
		{"-start", "starts with hyphen"},
		{"end-", "ends with hyphen"},
		{"-", "single hyphen"},
		{"foo/../../etc/passwd", "path traversal"},
		{strings.Repeat("a", 64), "too long (64 chars)"},
		{"foo bar", "space in middle"},
		{"hello world!", "exclamation"},
		{"service.name", "dot"},
	}
	for _, tc := range invalid {
		if err := ValidateServiceName(tc.name); err == nil {
			t.Errorf("ValidateServiceName(%q) [%s] = nil, want error", tc.name, tc.desc)
		}
	}
}

func TestValidateServiceName_MaxLength(t *testing.T) {
	// 63 chars should be valid
	name63 := strings.Repeat("a", 63)
	if err := ValidateServiceName(name63); err != nil {
		t.Errorf("ValidateServiceName(63 chars) = %v, want nil", err)
	}

	// 64 chars should be invalid
	name64 := strings.Repeat("a", 64)
	if err := ValidateServiceName(name64); err == nil {
		t.Error("ValidateServiceName(64 chars) = nil, want error")
	}
}
