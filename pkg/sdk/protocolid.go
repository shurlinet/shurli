package sdk

import (
	"fmt"
	"strings"
)

// ProtocolPrefix is the base prefix for all Shurli application-layer protocols.
const ProtocolPrefix = "/shurli"

// ProtocolID constructs a validated Shurli protocol identifier.
// Format: /shurli/<name>/<version>
// Panics on invalid input (empty, contains slash or whitespace).
// Use at init time for protocol constants; SDK consumers use this
// to register new protocols that are guaranteed well-formed.
func ProtocolID(name, version string) string {
	if name == "" || version == "" {
		panic(fmt.Sprintf("ProtocolID: name and version must be non-empty (got name=%q, version=%q)", name, version))
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(version, '/') {
		panic(fmt.Sprintf("ProtocolID: name and version must not contain '/' (got name=%q, version=%q)", name, version))
	}
	if strings.ContainsAny(name, " \t\n\r") || strings.ContainsAny(version, " \t\n\r") {
		panic(fmt.Sprintf("ProtocolID: name and version must not contain whitespace (got name=%q, version=%q)", name, version))
	}
	return ProtocolPrefix + "/" + name + "/" + version
}

// ValidateProtocolID checks whether id is a well-formed Shurli protocol ID.
// Returns nil if valid, or an error describing the problem.
func ValidateProtocolID(id string) error {
	if !strings.HasPrefix(id, ProtocolPrefix+"/") {
		return fmt.Errorf("protocol ID %q does not start with %q", id, ProtocolPrefix+"/")
	}
	rest := strings.TrimPrefix(id, ProtocolPrefix+"/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("protocol ID %q must have format %s/<name>/<version>", id, ProtocolPrefix)
	}
	if strings.ContainsAny(parts[0], " \t\n\r") {
		return fmt.Errorf("protocol ID %q has whitespace in name", id)
	}
	if strings.ContainsAny(parts[1], " \t\n\r") {
		return fmt.Errorf("protocol ID %q has whitespace in version", id)
	}
	return nil
}

// MustValidateProtocolIDs validates a batch of protocol IDs at init time.
// Panics if any ID is malformed. Call from init() to catch typos at startup.
func MustValidateProtocolIDs(ids ...string) {
	for _, id := range ids {
		if err := ValidateProtocolID(id); err != nil {
			panic(fmt.Sprintf("invalid protocol ID: %v", err))
		}
	}
}
