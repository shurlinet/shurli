package importcheck

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		path    string
		allowed bool
	}{
		{"github.com/shurlinet/shurli/internal/config", true},
		{"github.com/shurlinet/shurli/internal/daemon", true},
		{"github.com/shurlinet/shurli/internal/termcolor", true},
		{"github.com/shurlinet/shurli/internal/vault", false},
		{"github.com/shurlinet/shurli/internal/identity", false},
		{"github.com/shurlinet/shurli/internal/auth", false},
		{"github.com/shurlinet/shurli/internal/macaroon", false},
		{"github.com/shurlinet/shurli/internal/relay", false},
	}

	internalPrefix := modulePath + "/internal"

	for _, tt := range tests {
		if !strings.HasPrefix(tt.path, internalPrefix) {
			continue
		}
		got := isAllowed(tt.path)
		if got != tt.allowed {
			t.Errorf("isAllowed(%q) = %v, want %v", tt.path, got, tt.allowed)
		}
	}
}

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "github.com/shurlinet/shurli/plugins/testplugin")
}
