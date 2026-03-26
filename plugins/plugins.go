// Package plugins registers all compiled-in plugins with the registry.
package plugins

import (
	"fmt"

	"github.com/shurlinet/shurli/pkg/plugin"
	"github.com/shurlinet/shurli/plugins/filetransfer"
)

// RegisterAll registers all compiled-in plugins with the given registry.
// Called during daemon startup after the registry is created.
// P17 fix: returns error instead of silently discarding registration failures.
func RegisterAll(r *plugin.Registry) error {
	if err := r.Register(filetransfer.New()); err != nil {
		return fmt.Errorf("register filetransfer: %w", err)
	}
	return nil
}

// RegisterCLI registers CLI commands for all compiled-in plugins.
// Called from main.go at startup. Uses config to check enabled state.
func RegisterCLI() {
	filetransfer.RegisterCLI()
}
