package fakecmd

import (
	_ "github.com/shurlinet/shurli/plugins/fakeplugin" // want `cmd/shurli/cmd_violations.go must not import .* directly`
)

var Y = 1
