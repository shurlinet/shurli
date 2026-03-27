package sdk

import (
	"context"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerNetwork is the high-level interface for interacting with a Shurli P2P
// network. Third-party code and SDKs should depend on this interface, not the
// concrete Network struct.
//
// The concrete *Network satisfies this interface.
type PeerNetwork interface {
	// PeerID returns this node's peer ID.
	PeerID() peer.ID

	// ExposeService registers a local TCP service for remote peers.
	// Pass nil for allowedPeers to allow all authorized peers.
	ExposeService(name, localAddress string, allowedPeers map[peer.ID]struct{}) error

	// UnexposeService removes a previously exposed service.
	UnexposeService(name string) error

	// ListServices returns all registered services.
	ListServices() []*Service

	// ConnectToService connects to a remote peer's named service (30s timeout).
	ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error)

	// ConnectToServiceContext connects to a remote peer's named service.
	ConnectToServiceContext(ctx context.Context, peerID peer.ID, serviceName string) (ServiceConn, error)

	// ResolveName resolves a human-readable name to a peer ID.
	ResolveName(name string) (peer.ID, error)

	// RegisterName registers a local name-to-peer mapping.
	RegisterName(name string, peerID peer.ID) error

	// OnEvent registers an event handler. Returns a deregistration function.
	OnEvent(handler EventHandler) func()

	// Close shuts down the network.
	Close() error
}

// Resolver resolves human-readable names to peer IDs.
// Implementations can chain: local config -> DNS -> DHT -> blockchain.
//
// The concrete *NameResolver satisfies this interface.
type Resolver interface {
	Resolve(name string) (peer.ID, error)
}

// ServiceManager manages service registration and dialing.
//
// The concrete *ServiceRegistry satisfies this interface.
type ServiceManager interface {
	RegisterService(svc *Service) error
	UnregisterService(name string) error
	GetService(name string) (*Service, bool)
	ListServices() []*Service
	DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error)

	// Use adds stream middleware. Middleware wraps every stream handler
	// in the order added (first added = outermost wrapper).
	Use(middleware ...StreamMiddleware)
}

// Authorizer makes authorization decisions about peers.
// Implementations: file-based allowlist, database lookup, certificate chain, etc.
//
// The concrete *auth.AuthorizedPeerGater satisfies this interface.
type Authorizer interface {
	IsAuthorized(p peer.ID) bool
}

// StreamMiddleware wraps a stream handler to add cross-cutting behavior
// (compression, bandwidth limiting, progress tracking, audit trails).
//
// The handler receives the raw libp2p stream and the service name.
// Call next to continue the chain; skip it to short-circuit.
type StreamMiddleware func(next StreamHandler) StreamHandler

// StreamHandler processes an inbound libp2p stream for a named service.
type StreamHandler func(serviceName string, s network.Stream)

// EventType identifies the kind of network event.
type EventType int

const (
	EventPeerConnected    EventType = iota + 1 // A peer connected
	EventPeerDisconnected                      // A peer disconnected
	EventServiceRegistered                     // A service was registered
	EventServiceRemoved                        // A service was unregistered
	EventStreamOpened                          // An inbound stream was opened
	EventStreamClosed                          // A stream was closed
	EventAuthAllow                             // An inbound connection was allowed
	EventAuthDeny                              // An inbound connection was denied
	EventTransferPending                       // A transfer is awaiting approval (ask mode)
)

// Event carries details about a network event.
type Event struct {
	Type        EventType
	PeerID      peer.ID // Relevant peer (zero value if not applicable)
	ServiceName string  // Relevant service (empty if not applicable)
	Detail      string  // Additional context (e.g. transfer ID)
}

// EventHandler is a callback for network events.
// Handlers must be non-blocking; long work should be dispatched to a goroutine.
type EventHandler func(Event)

// Compile-time interface satisfaction checks.
var (
	_ PeerNetwork  = (*Network)(nil)
	_ Resolver     = (*NameResolver)(nil)
	_ ServiceManager = (*ServiceRegistry)(nil)
)
