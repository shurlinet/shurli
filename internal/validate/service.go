package validate

import (
	"fmt"
	"regexp"
)

// serviceNameRe matches DNS-label-style service names: 1-63 lowercase alphanumeric
// or hyphens, starting and ending with alphanumeric. Prevents protocol ID injection
// via names containing '/', newlines, or other special characters.
var serviceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ServiceName checks that a service name is safe for use in protocol IDs.
func ServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if !serviceNameRe.MatchString(name) {
		return fmt.Errorf("invalid service name %q: must be 1-63 lowercase alphanumeric characters or hyphens, starting and ending with alphanumeric", name)
	}
	return nil
}
