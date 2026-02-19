package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/satindergrewal/peer-up/internal/termcolor"
	"github.com/satindergrewal/peer-up/internal/validate"
)

func runService(args []string) {
	if len(args) < 1 {
		printServiceUsage()
		os.Exit(1)
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
		os.Exit(1)
	}
}

func printServiceUsage() {
	fmt.Println("Usage: peerup service <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add     <name> <address>  Expose a local service (enabled by default)")
	fmt.Println("  list                      List configured services")
	fmt.Println("  remove  <name>            Remove a service")
	fmt.Println("  enable  <name>            Enable a service")
	fmt.Println("  disable <name>            Disable a service")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  peerup service add ssh localhost:22")
	fmt.Println("  peerup service add ollama localhost:11434")
	fmt.Println("  peerup service add web localhost:8080 --protocol my-web")
	fmt.Println("  peerup service list")
	fmt.Println("  peerup service disable web")
	fmt.Println("  peerup service enable web")
	fmt.Println("  peerup service remove web")
	fmt.Println()
	fmt.Println("All commands support --config <path>.")
}

func runServiceAdd(args []string) {
	args = reorderArgs(args, nil)

	fs := flag.NewFlagSet("service add", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	protocolFlag := fs.String("protocol", "", "custom protocol ID (optional)")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Println("Usage: peerup service add <name> <local-address> [--protocol <id>]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  peerup service add ssh localhost:22")
		fmt.Println("  peerup service add ollama localhost:11434")
		fmt.Println("  peerup service add web localhost:8080 --protocol my-web")
		os.Exit(1)
	}

	name := fs.Arg(0)
	address := fs.Arg(1)

	// Validate service name (DNS-label format)
	if err := validate.ServiceName(name); err != nil {
		log.Fatalf("Invalid service name: %v", err)
	}

	// Validate address has host:port
	if _, _, err := net.SplitHostPort(address); err != nil {
		log.Fatalf("Invalid address %q: must be host:port (e.g., localhost:22)\n  Error: %v", address, err)
	}

	cfgFile, cfg := resolveConfigFile(*configFlag)

	// Check for duplicate
	if cfg.Services != nil {
		if _, exists := cfg.Services[name]; exists {
			termcolor.Yellow("Service already configured: %s", name)
			return
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
		log.Fatalf("Failed to read config: %v", err)
	}

	content := string(data)

	if strings.Contains(content, "services: {}") {
		// Replace empty services block
		content = strings.Replace(content, "services: {}", "services:\n"+block, 1)
	} else if strings.Contains(content, "services:") {
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
					// Next line exits services section (or we're at end) — insert here
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
		// No services section at all — append at end
		content += fmt.Sprintf("\nservices:\n%s\n", block)
	}

	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		log.Fatalf("Failed to write config: %v", err)
	}

	termcolor.Green("Added service: %s -> %s", name, address)
	fmt.Printf("Config: %s\n", cfgFile)
	fmt.Println()
	fmt.Println("Restart 'peerup daemon' to apply.")
}

func runServiceList(args []string) {
	fs := flag.NewFlagSet("service list", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgs(args, nil))

	cfgFile, cfg := resolveConfigFile(*configFlag)

	if cfg.Services == nil || len(cfg.Services) == 0 {
		fmt.Println("No services configured.")
		fmt.Printf("Config: %s\n", cfgFile)
		return
	}

	fmt.Printf("Services (%d):\n\n", len(cfg.Services))
	for name, svc := range cfg.Services {
		state := "enabled"
		if !svc.Enabled {
			state = "disabled"
		}
		proto := ""
		if svc.Protocol != "" {
			proto = fmt.Sprintf("  protocol: %s", svc.Protocol)
		}
		fmt.Printf("  %-12s -> %-20s (%s)%s\n", name, svc.LocalAddress, state, proto)
	}
	fmt.Printf("\nConfig: %s\n", cfgFile)
}

func runServiceSetEnabled(args []string, enabled bool) {
	args = reorderArgs(args, nil)

	fs := flag.NewFlagSet("service enable/disable", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if fs.NArg() != 1 {
		if enabled {
			fmt.Println("Usage: peerup service enable <name>")
		} else {
			fmt.Println("Usage: peerup service disable <name>")
		}
		os.Exit(1)
	}

	name := fs.Arg(0)
	cfgFile, cfg := resolveConfigFile(*configFlag)

	// Check it exists
	if cfg.Services == nil {
		log.Fatalf("Service not found: %s", name)
	}
	svc, exists := cfg.Services[name]
	if !exists {
		log.Fatalf("Service not found: %s", name)
	}

	// Already in desired state?
	if svc.Enabled == enabled {
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		termcolor.Yellow("Service %s is already %s", name, state)
		return
	}

	// Read config and flip the enabled field
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
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
		log.Fatalf("Could not find enabled field for service %q.\nPlease edit manually: %s", name, cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		log.Fatalf("Failed to write config: %v", err)
	}

	if enabled {
		termcolor.Green("Enabled service: %s", name)
	} else {
		termcolor.Yellow("Disabled service: %s", name)
	}
	fmt.Printf("Config: %s\n", cfgFile)
	fmt.Println()
	fmt.Println("Restart 'peerup daemon' to apply.")
}

func runServiceRemove(args []string) {
	args = reorderArgs(args, nil)

	fs := flag.NewFlagSet("service remove", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Println("Usage: peerup service remove <name>")
		os.Exit(1)
	}

	name := fs.Arg(0)
	cfgFile, cfg := resolveConfigFile(*configFlag)

	// Check it exists
	if cfg.Services == nil {
		log.Fatalf("Service not found: %s", name)
	}
	if _, exists := cfg.Services[name]; !exists {
		log.Fatalf("Service not found: %s", name)
	}

	// Read config and remove the service block
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
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
				// Empty line — could be between services or end of section.
				// Peek-skip: include it in removal to keep formatting clean.
				continue
			}
			if strings.HasPrefix(line, "    ") {
				// Child property line (4+ space indent) — skip
				continue
			}
			// Not a child line — stop skipping
			skipping = false
			inServices = false
		}

		result = append(result, line)
	}

	if !removed {
		log.Fatalf("Could not find service %q in config file.\nPlease remove manually from: %s", name, cfgFile)
	}

	// Check if services section is now empty — replace with "services: {}"
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
		log.Fatalf("Failed to write config: %v", err)
	}

	termcolor.Green("Removed service: %s", name)
	fmt.Printf("Config: %s\n", cfgFile)
	fmt.Println()
	fmt.Println("Restart 'peerup daemon' to apply.")
}
