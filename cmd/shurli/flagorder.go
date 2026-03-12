package main

import (
	"flag"
	"strings"
)

// reorderFlags moves flags (and their values) before positional arguments
// so that Go's flag.FlagSet.Parse works regardless of flag position.
//
// Go's stdlib flag package stops parsing at the first non-flag argument.
// This means "shurli send file.txt peer --follow" silently ignores --follow.
// Unix convention allows flags anywhere: "ls -la /tmp" and "ls /tmp -la" are equivalent.
func reorderFlags(fs *flag.FlagSet, args []string) []string {
	if len(args) == 0 {
		return args
	}

	// Build lookup of registered flag names and whether they're boolean.
	boolFlags := make(map[string]bool)
	allFlags := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		allFlags[f.Name] = true
		// Check if the flag value implements IsBoolFlag() bool.
		type boolFlagger interface {
			IsBoolFlag() bool
		}
		if bf, ok := f.Value.(boolFlagger); ok && bf.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})

	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// "--" marks end of flags; everything after is positional.
		if arg == "--" {
			positional = append(positional, args[i:]...)
			break
		}

		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)

		// Extract the flag name (strip leading dashes).
		name := strings.TrimLeft(arg, "-")

		// Handle --flag=value (value is already in the arg).
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			continue
		}

		// Bool flags don't consume the next argument.
		if boolFlags[name] {
			continue
		}

		// Non-bool flags consume the next argument as their value.
		if allFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}
