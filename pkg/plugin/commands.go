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
func RegisterCLICommand(entry CLICommandEntry) {
	cliMu.Lock()
	defer cliMu.Unlock()
	cliCommands[entry.Name] = &entry
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
func CLICommandDescriptions() []CLICommandEntry {
	cliMu.RLock()
	defer cliMu.RUnlock()

	cmds := make([]CLICommandEntry, 0, len(cliCommands))
	for _, entry := range cliCommands {
		cmds = append(cmds, *entry)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
