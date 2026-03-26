package plugin

import (
	"sort"
	"sync"
)

// CLIFlagEntry describes a CLI flag for dynamic completion generation.
type CLIFlagEntry struct {
	Long        string   // e.g. "follow"
	Short       string   // e.g. "f" (empty = no short flag)
	Description string   // e.g. "Follow transfer progress"
	Type        string   // "bool", "string", "int", "enum", "file", "directory"
	Enum        []string // non-nil only when Type="enum" (e.g. ["low","normal","high"])
	RequiresArg bool     // true if flag takes a value (non-bool flags)
}

// CLISubcommand describes a subcommand (e.g. "share add", "share remove").
type CLISubcommand struct {
	Name        string
	Description string
	Flags       []CLIFlagEntry
}

// CLICommandEntry describes a CLI command provided by a plugin.
type CLICommandEntry struct {
	Name        string
	Description string
	Usage       string
	PluginName  string           // which plugin provides this (for isPluginEnabled check)
	Run         func(args []string)
	Flags       []CLIFlagEntry   // for dynamic completion/man generation
	Subcommands []CLISubcommand  // for commands like "share" with add/remove/list
}

var (
	cliMu       sync.RWMutex
	cliCommands = make(map[string]*CLICommandEntry)
)

// RegisterCLICommand adds a CLI command to the global registry.
// L2 fix: validates command names to prevent shell injection in completion scripts.
func RegisterCLICommand(entry CLICommandEntry) {
	if !isValidCommandName(entry.Name) {
		return // silently reject invalid command names
	}
	cliMu.Lock()
	defer cliMu.Unlock()
	cliCommands[entry.Name] = &entry
}

// isValidCommandName checks that a command name is safe for shell completion scripts.
// Allows alphanumeric, hyphens only. No spaces, shell metacharacters, or path traversal.
func isValidCommandName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// UnregisterCLICommands removes all CLI commands for a plugin.
func UnregisterCLICommands(pluginName string) {
	cliMu.Lock()
	defer cliMu.Unlock()
	for name, entry := range cliCommands {
		if entry.PluginName == pluginName {
			delete(cliCommands, name)
		}
	}
}

// FindCLICommand looks up a CLI command by name.
func FindCLICommand(name string) (*CLICommandEntry, bool) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	cmd, ok := cliCommands[name]
	return cmd, ok
}

// CLICommandDescriptions returns all registered CLI commands sorted by name.
// L1 fix: deep-copies Flags and Subcommands slices so callers can't mutate originals.
func CLICommandDescriptions() []CLICommandEntry {
	cliMu.RLock()
	defer cliMu.RUnlock()

	cmds := make([]CLICommandEntry, 0, len(cliCommands))
	for _, entry := range cliCommands {
		cp := *entry
		// L1 fix: deep-copy slices so returned copies don't share memory with originals.
		if len(cp.Flags) > 0 {
			flags := make([]CLIFlagEntry, len(cp.Flags))
			copy(flags, cp.Flags)
			for i, f := range flags {
				if len(f.Enum) > 0 {
					enumCp := make([]string, len(f.Enum))
					copy(enumCp, f.Enum)
					flags[i].Enum = enumCp
				}
			}
			cp.Flags = flags
		}
		if len(cp.Subcommands) > 0 {
			subs := make([]CLISubcommand, len(cp.Subcommands))
			copy(subs, cp.Subcommands)
			for i, sub := range subs {
				if len(sub.Flags) > 0 {
					subFlags := make([]CLIFlagEntry, len(sub.Flags))
					copy(subFlags, sub.Flags)
					for j, sf := range subFlags {
						if len(sf.Enum) > 0 {
							enumCp := make([]string, len(sf.Enum))
							copy(enumCp, sf.Enum)
							subFlags[j].Enum = enumCp
						}
					}
					subs[i].Flags = subFlags
				}
			}
			cp.Subcommands = subs
		}
		cmds = append(cmds, cp)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
