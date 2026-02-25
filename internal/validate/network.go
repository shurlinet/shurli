package validate

import (
	"fmt"
	"regexp"
)

// networkNameRe matches DNS-label-style network names: 1-63 lowercase alphanumeric
// or hyphens, starting and ending with alphanumeric. This ensures namespace values
// are safe for use in DHT protocol prefixes (/shurli/<namespace>/kad/1.0.0).
var networkNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NetworkName checks that a network namespace is DNS-label safe.
func NetworkName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidNetworkName)
	}
	if !networkNameRe.MatchString(name) {
		return fmt.Errorf("%w: %q must be 1-63 lowercase alphanumeric characters or hyphens, starting and ending with alphanumeric", ErrInvalidNetworkName, name)
	}
	return nil
}
