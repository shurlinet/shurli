// Package importcheck provides a go/analysis analyzer that flags direct imports
// of internal/ packages from plugins/. Plugins should only depend on pkg/plugin,
// pkg/p2pnet, and a small set of allowed internal helpers.
//
// Allowed internal packages (Layer 1 compiled-in plugins only):
//   - internal/config    - shared config types
//   - internal/daemon    - HTTP response helpers (RespondJSON, ParseJSON, etc.)
//   - internal/termcolor - terminal color formatting for CLI output
package importcheck

import (
	"strings"

	"golang.org/x/tools/go/analysis"
)

const modulePath = "github.com/shurlinet/shurli"

// allowedInternal lists internal packages that Layer 1 plugins may import.
// These provide shared infrastructure that doesn't leak daemon secrets.
var allowedInternal = []string{
	modulePath + "/internal/config",
	modulePath + "/internal/daemon",
	modulePath + "/internal/termcolor",
}

var Analyzer = &analysis.Analyzer{
	Name: "importcheck",
	Doc:  "flags plugins/* packages that import internal/* (except allowed helpers)",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	pkgPath := pass.Pkg.Path()

	// Only check packages under plugins/.
	if !strings.HasPrefix(pkgPath, modulePath+"/plugins") {
		return nil, nil
	}

	internalPrefix := modulePath + "/internal"

	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(importPath, internalPrefix) {
				continue
			}
			if isAllowed(importPath) {
				continue
			}
			pass.Reportf(imp.Pos(), "plugin %q must not import internal package %q; use pkg/plugin or pkg/p2pnet interfaces instead",
				pkgPath, importPath)
		}
	}

	return nil, nil
}

func isAllowed(importPath string) bool {
	for _, allowed := range allowedInternal {
		if importPath == allowed || strings.HasPrefix(importPath, allowed+"/") {
			return true
		}
	}
	return false
}
