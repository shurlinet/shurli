package daemon

import "errors"

var (
	// ErrDaemonAlreadyRunning is returned when trying to start a daemon
	// while another instance is already running on the same socket.
	ErrDaemonAlreadyRunning = errors.New("daemon already running")

	// ErrDaemonNotRunning is returned when trying to connect to a daemon
	// that is not running (socket file does not exist).
	ErrDaemonNotRunning = errors.New("daemon not running")

	// ErrProxyNotFound is returned when trying to disconnect a proxy
	// that does not exist or has already been torn down.
	ErrProxyNotFound = errors.New("proxy not found")

	// ErrUnauthorized is returned when a request lacks valid authentication.
	ErrUnauthorized = errors.New("unauthorized")
)
