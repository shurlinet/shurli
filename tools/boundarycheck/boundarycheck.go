// Package boundarycheck provides a go/analysis analyzer that enforces plugin
// architecture boundaries. It checks import graphs and detects plugin-specific
// code living in the wrong package.
//
// Rules enforced:
//  1. pkg/sdk/ must NOT import from plugins/
//  2. plugins/ may only import pkg/sdk/, pkg/plugin/, and allowed internal/ pkgs
//  3. cmd/shurli/ must not import plugins/ except in registration files
//  4. No plugin-specific receiver methods in pkg/sdk/
//  5. No plugin-specific protocol constants in pkg/sdk/
//
// Engine types and protocol constants are loaded from tools/plugin-engine-types.txt
// (not hardcoded). When a new plugin is extracted, add its types there.
//
// Known pre-existing violations are suppressed per-file via
// tools/known-boundary-violations.txt. Suppression is per-FILE, not per-directory.
package boundarycheck

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
)

const modulePath = "github.com/shurlinet/shurli"

// allowedInternalForPlugins lists internal packages that Layer 1 plugins may import.
// This duplicates tools/importcheck for the purpose of this analyzer; the authoritative
// check is importcheck. This analyzer focuses on boundary enforcement, not import policing.
// Kept in sync manually -- if they diverge, importcheck is authoritative.
var allowedInternalForPlugins = []string{
	modulePath + "/internal/config",
	modulePath + "/internal/daemon",
	modulePath + "/internal/termcolor",
}

// coreProtocolPrefixes lists protocol path prefixes that are CORE (not plugin-specific).
// Any /shurli/ protocol NOT matching these prefixes is considered plugin-specific.
var coreProtocolPrefixes = []string{
	"/shurli/kad/",
	"/shurli/relay-",
	"/shurli/peer-",
	"/shurli/zkp-",
	"/shurli/ping/",
}

// config holds engine types and protocol constants loaded from plugin-engine-types.txt.
type config struct {
	engineTypes   map[string]bool // type names (e.g., "TransferService")
	protocolConsts map[string]bool // const names (e.g., "TransferProtocol")
}

var (
	suppressedOnce sync.Once
	suppressedMap  map[string]bool // per-file suppression

	configOnce sync.Once
	engineCfg  config

	repoRootOnce sync.Once
	repoRootPath string
)

func loadConfig(pass *analysis.Pass) {
	configOnce.Do(func() {
		engineCfg = config{
			engineTypes:   make(map[string]bool),
			protocolConsts: make(map[string]bool),
		}

		data := readRepoFile(pass, "tools/plugin-engine-types.txt")
		if data == "" {
			return
		}

		for _, line := range strings.Split(data, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "type:") {
				engineCfg.engineTypes[strings.TrimPrefix(line, "type:")] = true
			} else if strings.HasPrefix(line, "const:") {
				engineCfg.protocolConsts[strings.TrimPrefix(line, "const:")] = true
			}
			// grep: and receiver: entries are for the shell script only
		}
	})
}

func loadSuppressions(pass *analysis.Pass) {
	suppressedOnce.Do(func() {
		suppressedMap = make(map[string]bool)

		data := readRepoFile(pass, "tools/known-boundary-violations.txt")
		if data == "" {
			return
		}

		for _, line := range strings.Split(data, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			suppressedMap[line] = true
		}
	})
}

// readRepoFile locates and reads a file relative to the repo root.
// It finds the repo root by walking up from the first source file to find go.mod.
func readRepoFile(pass *analysis.Pass, relPath string) string {
	repoRoot := findRepoRoot(pass)
	if repoRoot == "" {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(repoRoot, relPath))
	if err != nil {
		return ""
	}
	return string(data)
}

func findRepoRoot(pass *analysis.Pass) string {
	repoRootOnce.Do(func() {
		if len(pass.Files) == 0 {
			return
		}

		pos := pass.Fset.Position(pass.Files[0].Pos())
		dir := filepath.Dir(pos.Filename)

		// Walk up at most 20 levels to find go.mod
		for i := 0; i < 20; i++ {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				repoRootPath = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break // reached filesystem root
			}
			dir = parent
		}
	})
	return repoRootPath
}

// isFileSuppressed checks if a specific source file is in the suppression list.
// Suppression is per-FILE, not per-directory. A suppressed transfer.go does NOT
// suppress a newly added new_engine.go in the same directory.
func isFileSuppressed(pass *analysis.Pass, filePos ast.Node) bool {
	pos := pass.Fset.Position(filePos.Pos())
	absPath := pos.Filename

	repoRoot := findRepoRoot(pass)
	if repoRoot == "" {
		return false
	}

	relPath := strings.TrimPrefix(absPath, repoRoot+"/")
	return suppressedMap[relPath]
}

var Analyzer = &analysis.Analyzer{
	Name: "boundarycheck",
	Doc:  "enforces plugin architecture boundaries: import graphs, protocol isolation, engine placement",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	loadSuppressions(pass)
	loadConfig(pass)
	pkgPath := pass.Pkg.Path()

	switch {
	case strings.HasPrefix(pkgPath, modulePath+"/pkg/sdk"):
		return nil, checkSDK(pass, pkgPath)

	case strings.HasPrefix(pkgPath, modulePath+"/plugins"):
		return nil, checkPlugin(pass, pkgPath)

	case strings.HasPrefix(pkgPath, modulePath+"/cmd/shurli"):
		return nil, checkCmd(pass, pkgPath)
	}

	return nil, nil
}

// checkSDK enforces:
//   - pkg/sdk/ must not import plugins/
//   - No plugin engine methods defined in pkg/sdk/ (per-file suppression)
//   - No plugin-specific protocol constants in pkg/sdk/ (per-file suppression)
func checkSDK(pass *analysis.Pass, pkgPath string) error {
	pluginsPrefix := modulePath + "/plugins"

	for _, file := range pass.Files {
		fileSuppressed := isFileSuppressed(pass, file)

		// CHECK: imports -- never suppressed, even for known files
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(importPath, pluginsPrefix) {
				pass.Reportf(imp.Pos(),
					"pkg/sdk must not import from plugins/: %q imports %q",
					pkgPath, importPath)
			}
		}

		// Skip engine checks for suppressed files
		if fileSuppressed {
			continue
		}

		// CHECK: plugin-specific receiver methods
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}

			recvType := receiverTypeName(fn.Recv.List[0].Type)
			if engineCfg.engineTypes[recvType] {
				pass.Reportf(fn.Pos(),
					"plugin engine method %s.%s() is defined in pkg/sdk/ but belongs in a plugin package",
					recvType, fn.Name.Name)
			}
		}

		// CHECK: plugin-specific protocol constants
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !engineCfg.protocolConsts[name.Name] {
						continue
					}
					// Verify it's actually a protocol string with /shurli/ prefix
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
							val := strings.Trim(lit.Value, `"`)
							if strings.HasPrefix(val, "/shurli/") && !isCoreProtocol(val) {
								pass.Reportf(vs.Pos(),
									"plugin-specific protocol constant %s = %s is defined in pkg/sdk/ but belongs in a plugin package",
									name.Name, lit.Value)
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// checkPlugin enforces: plugins/ may only import from allowed packages.
func checkPlugin(pass *analysis.Pass, pkgPath string) error {
	internalPrefix := modulePath + "/internal"

	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)

			// Skip non-module imports (stdlib, third-party)
			if !strings.HasPrefix(importPath, modulePath) {
				continue
			}

			// Allowed: pkg/sdk, pkg/plugin
			if strings.HasPrefix(importPath, modulePath+"/pkg/sdk") ||
				strings.HasPrefix(importPath, modulePath+"/pkg/plugin") {
				continue
			}

			// Allowed: specific internal packages (Layer 1 only)
			if strings.HasPrefix(importPath, internalPrefix) {
				if isAllowedInternal(importPath) {
					continue
				}
				pass.Reportf(imp.Pos(),
					"plugin %q imports forbidden internal package %q; use pkg/plugin or pkg/sdk interfaces instead",
					pkgPath, importPath)
				continue
			}

			// Allowed: other plugins (for plugin dependencies)
			if strings.HasPrefix(importPath, modulePath+"/plugins") {
				continue
			}

			// Disallow: cmd/, tools/, test/
			if strings.HasPrefix(importPath, modulePath+"/cmd") ||
				strings.HasPrefix(importPath, modulePath+"/tools") ||
				strings.HasPrefix(importPath, modulePath+"/test") {
				pass.Reportf(imp.Pos(),
					"plugin %q must not import %q; plugins may only import pkg/sdk, pkg/plugin, and allowed internal packages",
					pkgPath, importPath)
			}
		}
	}

	return nil
}

// checkCmd enforces: cmd/shurli/ must not import plugins/ except in
// registration files (main.go, serve_common.go, cmd_daemon.go).
func checkCmd(pass *analysis.Pass, pkgPath string) error {
	pluginsPrefix := modulePath + "/plugins"

	for _, file := range pass.Files {
		fileName := filepath.Base(pass.Fset.File(file.Pos()).Name())

		// Registration files are allowed to import plugins
		if isRegistrationFile(fileName) {
			continue
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(importPath, pluginsPrefix) {
				pass.Reportf(imp.Pos(),
					"cmd/shurli/%s must not import %q directly; use plugin registry for dispatch",
					fileName, importPath)
			}
		}
	}

	return nil
}

// isRegistrationFile returns true for cmd/shurli/ files that are allowed
// to import from plugins/ for plugin registration and lifecycle.
func isRegistrationFile(fileName string) bool {
	switch fileName {
	case "main.go", "serve_common.go", "cmd_daemon.go":
		return true
	}
	return false
}

// isCoreProtocol returns true if the protocol path is a core Shurli protocol
// (not plugin-specific).
func isCoreProtocol(path string) bool {
	for _, prefix := range coreProtocolPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// receiverTypeName extracts the type name from a receiver expression,
// handling both *T and T forms.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func isAllowedInternal(importPath string) bool {
	for _, allowed := range allowedInternalForPlugins {
		if importPath == allowed || strings.HasPrefix(importPath, allowed+"/") {
			return true
		}
	}
	return false
}
