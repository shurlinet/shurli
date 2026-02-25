package main

import "strings"

// reorderArgs moves flags before positional arguments so Go's flag
// parser sees them regardless of order. boolFlags names flags that
// take no value (e.g., "json"). All other flags are assumed to
// consume the next argument as their value.
//
// Examples:
//
//	reorderArgs(["laptop", "--json", "-c", "3"], {"json": true})
//	→ ["--json", "-c", "3", "laptop"]
func reorderArgs(args []string, boolFlags map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)

			// --flag=value: value is already in the arg, nothing to consume
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				continue
			}

			// Boolean flag: no value to consume
			if boolFlags[name] {
				continue
			}

			// Value flag: consume the next arg
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return append(flags, positional...)
}
