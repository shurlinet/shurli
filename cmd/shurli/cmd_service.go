package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/validate"
)

func runService(args []string) {
	if len(args) < 1 {
		printServiceUsage()
		osExit(1)
	}

	switch args[0] {
	case "add":
		runServiceAdd(args[1:])
	case "list":
		runServiceList(args[1:])
	case "remove":
		runServiceRemove(args[1:])
	case "enable":
		runServiceSetEnabled(args[1:], true)
	case "disable":
		runServiceSetEnabled(args[1:], false)
	default:
		fmt.Fprintf(os.Stderr, "Unknown service command: %s\n\n", args[0])
		printServiceUsage()
		osExit(1)
	}
}

func printServiceUsage() {
	fmt.Println("Usage: shurli service <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add     <name> <address>  Expose a local service (enabled by default)")
	fmt.Println("  list                      List configured services")
	fmt.Println("  remove  <name>            Remove a service")
	fmt.Println("  enable  <name>            Enable a service")
	fmt.Println("  disable <name>            Disable a service")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  shurli service add ssh localhost:22")
	fmt.Println("  shurli service add ollama localhost:11434")
	fmt.Println("  shurli service add web localhost:8080 --protocol my-web")
	fmt.Println("  shurli service list")
	fmt.Println("  shurli service disable web")
	fmt.Println("  shurli service enable web")
	fmt.Println("  shurli service remove web")
	fmt.Println()
	fmt.Println("All commands support --config <path>.")
}

func runServiceAdd(args []string) {
	if err := doServiceAdd(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doServiceAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	protocolFlag := fs.String("protocol", "", "custom protocol ID (optional)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() < 2 {
		return fmt.Errorf("usage: shurli service add <name> <local-address> [--protocol <id>]")
	}

	name := fs.Arg(0)
	address := fs.Arg(1)

	// Validate service name (DNS-label format)
	if err := validate.ServiceName(name); err != nil {
		return fmt.Errorf("invalid service name: %w", err)
	}

	// Validate address has host:port
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid address %q: must be host:port (e.g., localhost:22)\n  Error: %v", address, err)
	}

	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	// Check for duplicate
	if cfg.Services != nil {
		if _, exists := cfg.Services[name]; exists {
			termcolor.Yellow("Service already configured: %s", name)
			return nil
		}
	}

	// Build the service YAML block
	var block string
	if *protocolFlag != "" {
		block = fmt.Sprintf("  %s:\n    enabled: true\n    local_address: \"%s\"\n    protocol: \"%s\"", name, address, *protocolFlag)
	} else {
		block = fmt.Sprintf("  %s:\n    enabled: true\n    local_address: \"%s\"", name, address)
	}

	// Read config file and insert service
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Check for an uncommented "services:" line (not "# services:" in comments).
	// The config template has commented-out service examples that must not match.
	hasUncommentedServices := false
	hasEmptyServices := strings.Contains(content, "services: {}")
	if !hasEmptyServices {
		for _, line := range strings.Split(content, "\n") {
			if strings.TrimSpace(line) == "services:" {
				hasUncommentedServices = true
				break
			}
		}
	}

	if hasEmptyServices {
		// Replace empty services block
		content = strings.Replace(content, "services: {}", "services:\n"+block, 1)
	} else if hasUncommentedServices {
		// Find the services section and append after the last service entry
		lines := strings.Split(content, "\n")
		var result []string
		inserted := false
		inServices := false

		for i := 0; i < len(lines); i++ {
			result = append(result, lines[i])
			trimmed := strings.TrimSpace(lines[i])

			if !inserted && trimmed == "services:" {
				inServices = true
				continue
			}

			if inServices && !inserted {
				// We're inside the services block. Check if the next line
				// exits the services section (less indentation or new section).
				nextIsServiceContent := false
				if i+1 < len(lines) {
					nextTrimmed := strings.TrimSpace(lines[i+1])
					nextLine := lines[i+1]
					// Still in services if indented with 2+ spaces and not empty
					if nextTrimmed != "" && len(nextLine) > 0 && (nextLine[0] == ' ' || nextLine[0] == '\t') {
						nextIsServiceContent = true
					}
				}

				if !nextIsServiceContent {
					// Next line exits services section (or we're at end)  - insert here
					result = append(result, block)
					inserted = true
					inServices = false
				}
			}
		}

		if !inserted {
			// Fallback: append at end of services section
			result = append(result, block)
		}
		content = strings.Join(result, "\n")
	} else {
		// No services section at all  - append at end
		content += fmt.Sprintf("\nservices:\n%s\n", block)
	}

	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	termcolor.Green("Added service: %s -> %s", name, address)
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart 'shurli daemon' to apply.")
	return nil
}

func runServiceList(args []string) {
	if err := doServiceList(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doServiceList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	if cfg.Services == nil || len(cfg.Services) == 0 {
		fmt.Fprintln(stdout, "No services configured.")
		fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
		return nil
	}

	fmt.Fprintf(stdout, "Services (%d):\n\n", len(cfg.Services))
	for name, svc := range cfg.Services {
		state := "enabled"
		if !svc.Enabled {
			state = "disabled"
		}
		proto := ""
		if svc.Protocol != "" {
			proto = fmt.Sprintf("  protocol: %s", svc.Protocol)
		}
		fmt.Fprintf(stdout, "  %-12s -> %-20s (%s)%s\n", name, svc.LocalAddress, state, proto)
	}
	fmt.Fprintf(stdout, "\nConfig: %s\n", cfgFile)
	return nil
}

func runServiceSetEnabled(args []string, enabled bool) {
	if err := doServiceSetEnabled(args, enabled, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doServiceSetEnabled(args []string, enabled bool, stdout io.Writer) error {
	fs := flag.NewFlagSet("service enable/disable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		if enabled {
			return fmt.Errorf("usage: shurli service enable <name>")
		}
		return fmt.Errorf("usage: shurli service disable <name>")
	}

	name := fs.Arg(0)
	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	// Check it exists
	if cfg.Services == nil {
		return fmt.Errorf("service not found: %s", name)
	}
	svc, exists := cfg.Services[name]
	if !exists {
		return fmt.Errorf("service not found: %s", name)
	}

	// Already in desired state?
	if svc.Enabled == enabled {
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		termcolor.Yellow("Service %s is already %s", name, state)
		return nil
	}

	// Read config and flip the enabled field
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	oldVal := "enabled: true"
	newVal := "enabled: false"
	if enabled {
		oldVal = "enabled: false"
		newVal = "enabled: true"
	}

	// Find the service block and replace its enabled line
	lines := strings.Split(string(data), "\n")
	var result []string
	inServices := false
	inTarget := false
	replaced := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "services:" {
			inServices = true
			result = append(result, line)
			continue
		}

		if inServices && !inTarget && !replaced {
			// Look for target service name at 2-space indent
			if trimmed == name+":" && strings.HasPrefix(line, "  ") {
				inTarget = true
				result = append(result, line)
				continue
			}
		}

		if inTarget && !replaced {
			if trimmed == oldVal && strings.HasPrefix(line, "    ") {
				// Replace the enabled line, preserving indentation
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				result = append(result, indent+newVal)
				replaced = true
				inTarget = false
				continue
			}
			// If we hit a line that's not a child (not 4+ space indent), stop looking
			if trimmed != "" && !strings.HasPrefix(line, "    ") {
				inTarget = false
			}
		}

		result = append(result, line)
	}

	if !replaced {
		return fmt.Errorf("could not find enabled field for service %q.\nPlease edit manually: %s", name, cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if enabled {
		termcolor.Green("Enabled service: %s", name)
	} else {
		termcolor.Yellow("Disabled service: %s", name)
	}
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart 'shurli daemon' to apply.")
	return nil
}

func runServiceRemove(args []string) {
	if err := doServiceRemove(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doServiceRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli service remove <name>")
	}

	name := fs.Arg(0)
	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	// Check it exists
	if cfg.Services == nil {
		return fmt.Errorf("service not found: %s", name)
	}
	if _, exists := cfg.Services[name]; !exists {
		return fmt.Errorf("service not found: %s", name)
	}

	// Read config and remove the service block
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	removed := false
	inServices := false
	skipping := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "services:" {
			inServices = true
			result = append(result, line)
			continue
		}

		if inServices && !skipping {
			// Check if this line is the service name we want to remove.
			// Service names appear as "  <name>:" with 2-space indent under services.
			if trimmed == name+":" && strings.HasPrefix(line, "  ") {
				skipping = true
				removed = true
				continue
			}
		}

		if skipping {
			// Skip child lines (4+ space indent: properties of the service).
			// Stop skipping when we hit a line with 2-space indent (next service)
			// or less indent (next top-level section) or empty line before next section.
			if trimmed == "" {
				// Empty line  - could be between services or end of section.
				// Peek-skip: include it in removal to keep formatting clean.
				continue
			}
			if strings.HasPrefix(line, "    ") {
				// Child property line (4+ space indent)  - skip
				continue
			}
			// Not a child line  - stop skipping
			skipping = false
			inServices = false
		}

		result = append(result, line)
	}

	if !removed {
		return fmt.Errorf("could not find service %q in config file.\nPlease remove manually from: %s", name, cfgFile)
	}

	// Check if services section is now empty  - replace with "services: {}"
	content := strings.Join(result, "\n")
	remaining := 0
	for n := range cfg.Services {
		if n != name {
			remaining++
		}
	}
	if remaining == 0 {
		// Replace bare "services:" with "services: {}"
		content = strings.Replace(content, "\nservices:\n", "\nservices: {}\n", 1)
	}

	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	termcolor.Green("Removed service: %s", name)
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart 'shurli daemon' to apply.")
	return nil
}
