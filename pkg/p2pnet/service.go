package p2pnet

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// validServiceName matches DNS-label-style service names: 1-63 lowercase alphanumeric
// or hyphens, starting and ending with alphanumeric. Prevents protocol ID injection
// via names containing '/', newlines, or other special characters.
var validServiceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateServiceName checks that a service name is safe for use in protocol IDs.
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if !validServiceName.MatchString(name) {
		return fmt.Errorf("invalid service name %q: must be 1-63 lowercase alphanumeric characters or hyphens, starting and ending with alphanumeric", name)
	}
	return nil
}

// Service represents a service that can be exposed over the P2P network
type Service struct {
	Name         string // Service name (e.g., "ssh", "http")
	Protocol     string // libp2p protocol ID (e.g., "/peerup/ssh/1.0.0")
	LocalAddress string // Local TCP address (e.g., "localhost:22")
	Enabled      bool   // Whether this service is enabled
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
		return fmt.Errorf("service %s already registered", svc.Name)
	}

	// Register service
	r.services[svc.Name] = svc

	// Set up stream handler
	pid := protocol.ID(svc.Protocol)
	r.host.SetStreamHandler(pid, r.handleServiceStream(svc))

	log.Printf("✅ Registered service: %s (protocol: %s, local: %s)",
		svc.Name, svc.Protocol, svc.LocalAddress)

	return nil
}

// handleServiceStream creates a stream handler for a service
func (r *ServiceRegistry) handleServiceStream(svc *Service) func(network.Stream) {
	return func(s network.Stream) {
		remotePeer := s.Conn().RemotePeer()
		log.Printf("📥 Incoming %s connection from %s", svc.Name, remotePeer.String()[:16]+"...")

		// Connect to local service
		localConn, err := net.Dial("tcp", svc.LocalAddress)
		if err != nil {
			log.Printf("❌ Failed to connect to local service %s at %s: %v",
				svc.Name, svc.LocalAddress, err)
			s.Reset()
			return
		}

		// Bidirectional proxy with half-close propagation
		tcpDone := make(chan struct{})
		streamDone := make(chan struct{})

		// Local TCP -> Stream
		go func() {
			defer close(tcpDone)
			_, err := io.Copy(s, localConn)
			if err != nil && err != io.EOF {
				log.Printf("⚠️  Local→Stream copy error for %s: %v", svc.Name, err)
			}
			// Signal remote peer: no more data coming from this side
			s.CloseWrite()
		}()

		// Stream -> Local TCP
		go func() {
			defer close(streamDone)
			_, err := io.Copy(localConn, s)
			if err != nil && err != io.EOF {
				log.Printf("⚠️  Stream→Local copy error for %s: %v", svc.Name, err)
			}
			// Signal local service: no more data coming from this side
			if tc, ok := localConn.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
		}()

		// Wait for both directions to finish (safe: each signals the other via CloseWrite)
		<-tcpDone
		<-streamDone

		localConn.Close()
		s.Close()

		log.Printf("✅ Closed %s connection from %s", svc.Name, remotePeer.String()[:16]+"...")
	}
}

// DialService connects to a remote peer's service
func (r *ServiceRegistry) DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error) {
	pid := protocol.ID(protocolID)

	log.Printf("📤 Connecting to peer %s service %s...", peerID.String()[:16]+"...", protocolID)

	// Open stream to remote peer
	s, err := r.host.NewStream(ctx, peerID, pid)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	log.Printf("✅ Connected to peer %s service %s", peerID.String()[:16]+"...", protocolID)

	return &serviceStream{stream: s}, nil
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
