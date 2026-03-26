package plugin

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoPluginImportsInternal verifies that plugin packages only import
// allowed internal packages. Layer 1 compiled-in plugins (like filetransfer)
// currently import some internal packages for CLI/daemon integration.
// These are tracked and must not grow without review.
//
// Layer 2 WASM plugins will have zero internal imports (enforced by sandbox).
func TestNoPluginImportsInternal(t *testing.T) {
	pluginsDir := filepath.Join("..", "..", "plugins")

	// Layer 1 allowlist: internal packages that compiled-in plugins are
	// allowed to import. Each entry is tracked and justified.
	allowed := map[string]bool{
		"github.com/shurlinet/shurli/internal/config":    true, // client config
		"github.com/shurlinet/shurli/internal/termcolor": true, // CLI coloring
		"github.com/shurlinet/shurli/internal/daemon":    true, // daemon client
	}

	var violations []string

	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("failed to parse %s: %v", path, parseErr)
			return nil
		}

		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)

			if strings.Contains(importPath, "shurli") && strings.Contains(importPath, "/internal/") {
				if !allowed[importPath] {
					violations = append(violations, path+": "+importPath)
				}
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("failed to walk plugins directory: %v", err)
	}

	for _, v := range violations {
		t.Errorf("unexpected internal import: %s", v)
	}
}
