// Package plugins registers all compiled-in plugins with the registry.
//
// In Batch 1 this is empty - no plugins are extracted yet.
// Batch 2 will add the file transfer plugin:
//
//	import _ "github.com/shurlinet/shurli/plugins/filetransfer"
//
// Each compiled-in plugin registers itself via its init() function or
// is explicitly registered here in RegisterAll().
package plugins

import "github.com/shurlinet/shurli/pkg/plugin"

// RegisterAll registers all compiled-in plugins with the given registry.
// Called during daemon startup after the registry is created.
func RegisterAll(_ *plugin.Registry) {
	// Batch 2 will add:
	// r.Register(filetransfer.New())
}
