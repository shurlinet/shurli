package p2pnet

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/satindergrewal/peer-up/internal/validate"
)

// connectionTag returns "[RELAYED]" or "[DIRECT]" based on the stream's connection path.
func connectionTag(s network.Stream) string {
	addr := s.Conn().RemoteMultiaddr().String()
	if strings.Contains(addr, "/p2p-circuit") {
		return "[RELAYED]"
	}
	return "[DIRECT]"
}

// ValidateServiceName checks that a service name is safe for use in protocol IDs.
func ValidateServiceName(name string) error {
	return validate.ServiceName(name)
}

// Service represents a service that can be exposed over the P2P network
type Service struct {
	Name         string              // Service name (e.g., "ssh", "http")
	Protocol     string              // libp2p protocol ID (e.g., "/peerup/ssh/1.0.0")
	LocalAddress string              // Local TCP address (e.g., "localhost:22")
	Enabled      bool                // Whether this service is enabled
	AllowedPeers map[peer.ID]struct{} // Per-service ACL (nil = all authorized peers allowed)
}

// ServiceConn represents a connection to a remote service
type ServiceConn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// ServiceRegistry manages service registration and connections
type ServiceRegistry struct {
	host     host.Host
	services map[string]*Service
	mu       sync.RWMutex
}

// NewServiceRegistry creates a new service registry
func NewServiceRegistry(h host.Host) *ServiceRegistry {
	return &ServiceRegistry{
		host:     h,
		services: make(map[string]*Service),
	}
}

// RegisterService registers a new service and sets up its stream handler
func (r *ServiceRegistry) RegisterService(svc *Service) error {
	if svc == nil {
		return fmt.Errorf("service cannot be nil")
	}

	if svc.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}

	if svc.LocalAddress == "" {
		return fmt.Errorf("service local_address cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if service already registered
	if _, exists := r.services[svc.Name]; exists {
		return fmt.Errorf("%w: %s", ErrServiceAlreadyRegistered, svc.Name)
	}

	// Register service
	r.services[svc.Name] = svc

	// Set up stream handler
	pid := protocol.ID(svc.Protocol)
	r.host.SetStreamHandler(pid, r.handleServiceStream(svc))

	slog.Info("registered service", "service", svc.Name, "protocol", svc.Protocol, "local", svc.LocalAddress)

	return nil
}

// handleServiceStream creates a stream handler for a service
func (r *ServiceRegistry) handleServiceStream(svc *Service) func(network.Stream) {
	return func(s network.Stream) {
		remotePeer := s.Conn().RemotePeer()
		tag := connectionTag(s)
		short := remotePeer.String()[:16] + "..."
		slog.Info("incoming connection", "path", tag, "service", svc.Name, "peer", short)

		// Per-service access control
		if svc.AllowedPeers != nil {
			if _, ok := svc.AllowedPeers[remotePeer]; !ok {
				slog.Warn("peer not in service ACL", "service", svc.Name, "peer", short)
				s.Reset()
				return
			}
		}

		// Connect to local service (with timeout to avoid hanging on unreachable services)
		localConn, err := net.DialTimeout("tcp", svc.LocalAddress, 10*time.Second)
		if err != nil {
			slog.Error("failed to connect to local service", "service", svc.Name, "addr", svc.LocalAddress, "error", err)
			s.Reset()
			return
		}

		// Bidirectional proxy with half-close propagation
		BidirectionalProxy(&serviceStream{stream: s}, &tcpHalfCloser{localConn}, svc.Name)

		slog.Info("closed connection", "service", svc.Name, "peer", short)
	}
}

// DialService connects to a remote peer's service
func (r *ServiceRegistry) DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error) {
	pid := protocol.ID(protocolID)

	slog.Info("dialing service", "peer", peerID.String()[:16]+"...", "protocol", protocolID)

	// Allow limited (relay circuit) connections  - without this, NewStream
	// refuses to use relay circuits and only tries direct dials, which fail
	// when hole punching isn't possible (e.g., carrier-grade NAT on 5G).
	relayCtx := network.WithAllowLimitedConn(ctx, protocolID)

	// Open stream to remote peer
	s, err := r.host.NewStream(relayCtx, peerID, pid)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	tag := connectionTag(s)
	slog.Info("connected to peer", "path", tag, "peer", peerID.String()[:16]+"...", "protocol", protocolID)

	return &serviceStream{stream: s}, nil
}

// UnregisterService removes a service and its stream handler.
func (r *ServiceRegistry) UnregisterService(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	svc, exists := r.services[name]
	if !exists {
		return fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}

	// Remove stream handler
	r.host.RemoveStreamHandler(protocol.ID(svc.Protocol))

	// Remove from registry
	delete(r.services, name)

	slog.Info("unregistered service", "service", name, "protocol", svc.Protocol)
	return nil
}

// GetService retrieves a registered service by name
func (r *ServiceRegistry) GetService(name string) (*Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	svc, exists := r.services[name]
	return svc, exists
}

// ListServices returns all registered services
func (r *ServiceRegistry) ListServices() []*Service {
	r.mu.RLock()
	defer r.mu.RUnlock()

	services := make([]*Service, 0, len(r.services))
	for _, svc := range r.services {
		services = append(services, svc)
	}

	return services
}

// serviceStream wraps a libp2p stream to implement ServiceConn
type serviceStream struct {
	stream network.Stream
}

func (s *serviceStream) Read(p []byte) (n int, err error) {
	return s.stream.Read(p)
}

func (s *serviceStream) Write(p []byte) (n int, err error) {
	return s.stream.Write(p)
}

func (s *serviceStream) Close() error {
	return s.stream.Close()
}

func (s *serviceStream) CloseWrite() error {
	return s.stream.CloseWrite()
}
