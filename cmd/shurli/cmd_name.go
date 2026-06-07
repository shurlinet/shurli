package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
)

func runName(args []string) {
	if len(args) == 0 {
		printNameUsage(os.Stderr)
		osExit(1)
	}
	var err error
	switch args[0] {
	case "list":
		err = doNameList(args[1:], os.Stdout)
	case "add", "set":
		err = doNameAdd(args[1:], os.Stdout)
	case "remove", "rm":
		err = doNameRemove(args[1:], os.Stdout)
	case "help", "--help", "-h":
		printNameUsage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "Unknown name command: %s\n\n", args[0])
		printNameUsage(os.Stderr)
		osExit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func printNameUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: shurli name <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  list                       List all named peers")
	fmt.Fprintln(w, "  add    <name> <peer-id>    Add or update a name mapping")
	fmt.Fprintln(w, "  remove <name>              Remove a name mapping")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  shurli name list")
	fmt.Fprintln(w, "  shurli name add home-node 12D3KooW...")
	fmt.Fprintln(w, "  shurli name remove kaks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "All commands support --config <path>.")
}

func doNameList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("name list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	if cfg.Names == nil || len(cfg.Names) == 0 {
		fmt.Fprintln(stdout, "No names configured.")
		fmt.Fprintf(stdout, "\nConfig: %s\n", cfgFile)
		return nil
	}

	fmt.Fprintf(stdout, "Names (%d):\n\n", len(cfg.Names))
	for name, peerIDStr := range cfg.Names {
		short := peerIDStr
		if len(short) > 16 {
			short = short[:16] + "..."
		}
		fmt.Fprintf(stdout, "  %-16s -> %s\n", name, short)
	}
	fmt.Fprintf(stdout, "\nConfig: %s\n", cfgFile)
	return nil
}

func doNameAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("name add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 2 {
		return fmt.Errorf("usage: shurli name add <name> <peer-id>")
	}

	name := fs.Arg(0)
	peerIDStr := fs.Arg(1)

	// Validate peer ID
	if _, err := peer.Decode(peerIDStr); err != nil {
		return fmt.Errorf("invalid peer ID: %w", err)
	}

	cfgFile, _, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Check for exact duplicate
	entry := fmt.Sprintf("%s: \"%s\"", name, peerIDStr)
	if strings.Contains(content, entry) {
		fmt.Fprintf(stdout, "Name already exists: %s\n", name)
		return nil
	}

	lines := strings.Split(content, "\n")

	// Find the top-level "names:" section (indent level 0).
	// Skip nested names: sections (e.g., under relay config) by checking indent.
	topNamesIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "names:" || strings.HasPrefix(trimmed, "names: ") {
			lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			if lineIndent == 0 {
				topNamesIdx = i
				break
			}
		}
	}

	if topNamesIdx < 0 {
		// No top-level names section - append one
		content += fmt.Sprintf("\nnames:\n    %s: \"%s\"\n", name, peerIDStr)
	} else if strings.TrimSpace(lines[topNamesIdx]) == "names: {}" {
		// Replace empty names block
		lines[topNamesIdx] = fmt.Sprintf("names:\n    %s: \"%s\"", name, peerIDStr)
		content = strings.Join(lines, "\n")
	} else {
		// Find existing entries under top-level names: to detect indent
		indent := "    " // default
		lastEntryIdx := topNamesIdx

		for i := topNamesIdx + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			lineIndent := len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
			if lineIndent == 0 {
				// Hit next top-level section
				break
			}
			if strings.Contains(trimmed, ": \"") || strings.Contains(trimmed, ": '") {
				indent = lines[i][:lineIndent]
				lastEntryIdx = i
				// If this entry has the same name, replace it (update)
				if strings.HasPrefix(trimmed, name+":") {
					lines[i] = fmt.Sprintf("%s%s: \"%s\"", indent, name, peerIDStr)
					content = strings.Join(lines, "\n")
					goto write
				}
			}
		}

		// Insert after last entry
		var result []string
		for i, line := range lines {
			result = append(result, line)
			if i == lastEntryIdx {
				result = append(result, fmt.Sprintf("%s%s: \"%s\"", indent, name, peerIDStr))
			}
		}
		content = strings.Join(result, "\n")
	}

write:
	if err := auth.WriteFilePreserveOwnership(cfgFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Verify
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config corrupted after write: %w", err)
	}
	if cfg.Names[name] != peerIDStr {
		return fmt.Errorf("name was not written (check config manually)")
	}

	short := peerIDStr
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	fmt.Fprintf(stdout, "Added name: %s -> %s\n", name, short)
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)

	tryDaemonConfigReload()
	return nil
}

func doNameRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("name remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli name remove <name>")
	}

	name := fs.Arg(0)
	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	if cfg.Names == nil {
		return fmt.Errorf("name not found: %s", name)
	}
	if _, exists := cfg.Names[name]; !exists {
		return fmt.Errorf("name not found: %s", name)
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	removed := false
	inNames := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Only match top-level names: (indent 0)
		if !inNames && (trimmed == "names:" || strings.HasPrefix(trimmed, "names: ")) {
			lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			if lineIndent == 0 {
				inNames = true
			}
			result = append(result, line)
			continue
		}

		if inNames && !removed {
			lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			// Hit next top-level section
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && lineIndent == 0 {
				inNames = false
			} else if strings.HasPrefix(trimmed, name+":") {
				removed = true
				continue
			}
		}

		result = append(result, line)
	}

	if !removed {
		return fmt.Errorf("name not found in config file: %s", name)
	}

	content := strings.Join(result, "\n")
	if err := auth.WriteFilePreserveOwnership(cfgFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Fprintf(stdout, "Removed name: %s\n", name)
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)

	tryDaemonConfigReload()
	return nil
}
