package validate

import (
	"strings"
	"testing"
)

func TestServiceName(t *testing.T) {
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
		if err := ServiceName(name); err != nil {
			t.Errorf("ServiceName(%q) = %v, want nil", name, err)
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
		if err := ServiceName(tc.name); err == nil {
			t.Errorf("ServiceName(%q) [%s] = nil, want error", tc.name, tc.desc)
		}
	}
}

func TestServiceName_MaxLength(t *testing.T) {
	// 63 chars should be valid
	name63 := strings.Repeat("a", 63)
	if err := ServiceName(name63); err != nil {
		t.Errorf("ServiceName(63 chars) = %v, want nil", err)
	}

	// 64 chars should be invalid
	name64 := strings.Repeat("a", 64)
	if err := ServiceName(name64); err == nil {
		t.Error("ServiceName(64 chars) = nil, want error")
	}
}
