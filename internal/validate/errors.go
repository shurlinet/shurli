package validate

import "errors"

var (
	// ErrInvalidServiceName is returned when a service name does not match
	// the DNS-label format (1-63 lowercase alphanumeric + hyphens).
	ErrInvalidServiceName = errors.New("invalid service name")

	// ErrInvalidNetworkName is returned when a network namespace does not match
	// the DNS-label format (1-63 lowercase alphanumeric + hyphens).
	ErrInvalidNetworkName = errors.New("invalid network name")
)
