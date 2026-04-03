package testplugin

import (
	_ "github.com/shurlinet/shurli/internal/config" // allowed
	_ "github.com/shurlinet/shurli/internal/vault"  // want `plugin .* imports forbidden internal package`
)

var X = 1
