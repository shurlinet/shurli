package sdk

import (
	"errors"
	"fmt"
)

var (
	// ErrServiceAlreadyRegistered is returned when trying to register a service
	// that already exists in the registry.
	ErrServiceAlreadyRegistered = errors.New("service already registered")

	// ErrServiceNotFound is returned when trying to unregister or access a service
	// that does not exist in the registry.
	ErrServiceNotFound = errors.New("service not found")

	// ErrNameNotFound is returned when a name cannot be resolved to a peer ID
	// and is not a valid peer ID itself.
	ErrNameNotFound = errors.New("name not found")

	// ErrResponseTooLarge is returned when a protocol response exceeds the
	// maximum allowed size.
	ErrResponseTooLarge = errors.New("response too large")
)

// RemoteError wraps an error message returned by a remote peer.
type RemoteError struct {
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("remote: %s", e.Message)
}
