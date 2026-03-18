// Package plugins registers all compiled-in plugins with the registry.
package plugins

import (
	"github.com/shurlinet/shurli/pkg/plugin"
	"github.com/shurlinet/shurli/plugins/filetransfer"
)

// RegisterAll registers all compiled-in plugins with the given registry.
// Called during daemon startup after the registry is created.
func RegisterAll(r *plugin.Registry) {
	r.Register(filetransfer.New())
}

// RegisterCLI registers CLI commands for all compiled-in plugins.
// Called from main.go at startup. Uses config to check enabled state.
func RegisterCLI() {
	filetransfer.RegisterCLI()
}
