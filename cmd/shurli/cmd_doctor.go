package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shurlinet/shurli/internal/config"
)

func runDoctor(args []string) {
	if err := doDoctor(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

// checkResult represents a single doctor check.
type checkResult struct {
	name    string
	ok      bool
	message string
	fixable bool
}

func doDoctor(stdout io.Writer) error {
	fmt.Fprintln(stdout, "shurli doctor")
	fmt.Fprintln(stdout, "Checking your shurli installation...")
	fmt.Fprintln(stdout)

	var checks []checkResult

	// 1. Binary version.
	checks = append(checks, checkResult{
		name:    "Binary",
		ok:      true,
		message: fmt.Sprintf("shurli %s (%s) built %s", version, commit, buildDate),
	})

	// 2. Config file.
	checks = append(checks, checkConfig())

	// 3. Identity key.
	checks = append(checks, checkIdentity())

	// 4. Shell completion.
	checks = append(checks, checkShellCompletion())

	// 5. Man page.
	checks = append(checks, checkManPage())

	// Print results.
	fixable := 0
	problems := 0
	for _, c := range checks {
		if c.ok {
			fmt.Fprintf(stdout, "  [OK] %-20s %s\n", c.name, c.message)
		} else {
			fmt.Fprintf(stdout, "  [!!] %-20s %s\n", c.name, c.message)
			problems++
			if c.fixable {
				fixable++
			}
		}
	}

	fmt.Fprintln(stdout)

	if problems == 0 {
		fmt.Fprintln(stdout, "Everything looks good.")
		return nil
	}

	if fixable > 0 {
		fmt.Fprintf(stdout, "%d issue(s) found, %d auto-fixable.\n", problems, fixable)
		fmt.Fprintln(stdout, "Run: shurli doctor --fix")
		fmt.Fprintln(stdout)
	} else {
		fmt.Fprintf(stdout, "%d issue(s) found.\n", problems)
		fmt.Fprintln(stdout)
	}

	// If --fix was passed, fix what we can.
	for _, arg := range os.Args[2:] {
		if arg == "--fix" {
			return doctorFix(stdout)
		}
	}

	return nil
}

func doctorFix(stdout io.Writer) error {
	fmt.Fprintln(stdout, "Fixing...")
	fmt.Fprintln(stdout)
	setupShellEnvironment(stdout)
	return nil
}

// setupShellEnvironment detects the user's shell and installs completions + man page.
// Called by both "shurli init" and "shurli doctor --fix".
func setupShellEnvironment(stdout io.Writer) {
	shell := detectShell()

	// Install shell completion.
	if shell != "" {
		dest := completionInstallPath(shell)
		installed := tryInstallFile(dest, completionContent(shell))
		if installed {
			fmt.Fprintf(stdout, "Installed %s completion: %s\n", shell, dest)
		} else {
			fmt.Fprintf(stdout, "Shell completion: needs sudo to install to %s\n", dest)
			fmt.Fprintf(stdout, "  Run: sudo shurli completion %s --install\n", shell)
		}
	}

	// Install man page.
	dest := manInstallPath()
	installed := tryInstallFile(dest, manPage())
	if installed {
		fmt.Fprintf(stdout, "Installed man page: %s\n", dest)
	} else {
		fmt.Fprintf(stdout, "Man page: needs sudo to install to %s\n", dest)
		fmt.Fprintln(stdout, "  Run: sudo shurli man --install")
	}

	fmt.Fprintln(stdout)
}

// detectShell returns "bash", "zsh", or "fish" based on the SHELL env var.
// Returns empty string if unrecognized.
func detectShell() string {
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		return ""
	}
	base := filepath.Base(shellPath)
	switch base {
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	}
	return ""
}

// completionContent returns the completion script for the given shell.
func completionContent(shell string) string {
	switch shell {
	case "bash":
		return completionBash
	case "zsh":
		return completionZsh
	case "fish":
		return completionFish
	}
	return ""
}

// tryInstallFile attempts to write content to dest, creating parent dirs.
// Returns true on success, false if permission denied or other error.
func tryInstallFile(dest string, content string) bool {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false
	}
	if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
		return false
	}
	return true
}

// --- Doctor checks ---

func checkConfig() checkResult {
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		return checkResult{
			name:    "Config",
			ok:      false,
			message: "Not found. Run: shurli init",
		}
	}
	if _, err := config.LoadNodeConfig(cfgFile); err != nil {
		return checkResult{
			name:    "Config",
			ok:      false,
			message: fmt.Sprintf("Invalid: %v", err),
		}
	}
	return checkResult{
		name:    "Config",
		ok:      true,
		message: cfgFile,
	}
}

func checkIdentity() checkResult {
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		return checkResult{
			name:    "Identity",
			ok:      false,
			message: "No config found",
		}
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return checkResult{
			name:    "Identity",
			ok:      false,
			message: "Cannot load config",
		}
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))
	keyFile := cfg.Identity.KeyFile
	if keyFile == "" {
		keyFile = filepath.Join(filepath.Dir(cfgFile), "identity.key")
	}
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		return checkResult{
			name:    "Identity",
			ok:      false,
			message: fmt.Sprintf("Key file missing: %s", keyFile),
		}
	}
	return checkResult{
		name:    "Identity",
		ok:      true,
		message: keyFile,
	}
}

func checkShellCompletion() checkResult {
	shell := detectShell()
	if shell == "" {
		return checkResult{
			name:    "Completion",
			ok:      true,
			message: "Shell not detected (SHELL env var empty)",
		}
	}

	dest := completionInstallPath(shell)

	info, err := os.Stat(dest)
	if os.IsNotExist(err) {
		return checkResult{
			name:    "Completion",
			ok:      false,
			message: fmt.Sprintf("Not installed for %s (%s)", shell, dest),
			fixable: true,
		}
	}
	if err != nil {
		return checkResult{
			name:    "Completion",
			ok:      false,
			message: fmt.Sprintf("Cannot check %s: %v", dest, err),
		}
	}

	// Check if the installed version is stale by comparing size.
	// A version mismatch (new commands added, etc.) will change the script size.
	currentContent := completionContent(shell)
	if info.Size() != int64(len(currentContent)) {
		return checkResult{
			name:    "Completion",
			ok:      false,
			message: fmt.Sprintf("Outdated for %s (installed size %d, current %d)", shell, info.Size(), len(currentContent)),
			fixable: true,
		}
	}

	return checkResult{
		name:    "Completion",
		ok:      true,
		message: fmt.Sprintf("%s (%s)", shell, dest),
	}
}

func checkManPage() checkResult {
	dest := manInstallPath()

	if _, err := os.Stat(dest); os.IsNotExist(err) {
		// Also check if "man shurli" works anyway (could be installed elsewhere).
		if _, lookErr := exec.LookPath("man"); lookErr == nil {
			cmd := exec.Command("man", "-w", "shurli")
			if out, err := cmd.Output(); err == nil {
				path := strings.TrimSpace(string(out))
				return checkResult{
					name:    "Man page",
					ok:      true,
					message: path,
				}
			}
		}
		return checkResult{
			name:    "Man page",
			ok:      false,
			message: fmt.Sprintf("Not installed (%s)", dest),
			fixable: true,
		}
	}

	// Check if outdated (size comparison, same as completions).
	info, err := os.Stat(dest)
	if err != nil {
		return checkResult{
			name:    "Man page",
			ok:      false,
			message: fmt.Sprintf("Cannot check: %v", err),
		}
	}
	currentContent := manPage()
	if info.Size() != int64(len(currentContent)) {
		return checkResult{
			name:    "Man page",
			ok:      false,
			message: fmt.Sprintf("Outdated (installed size %d, current %d)", info.Size(), len(currentContent)),
			fixable: true,
		}
	}

	return checkResult{
		name:    "Man page",
		ok:      true,
		message: dest,
	}
}
