package sdk

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/validate"
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

// Service represents a service that can be exposed over the P2P network.
// Two modes are supported:
//   - TCP proxy: set LocalAddress to proxy streams to a local TCP service
//   - Custom handler: set Handler to process streams directly (for plugins)
//
// LocalAddress and Handler are mutually exclusive. If both are set,
// Handler takes precedence.
type Service struct {
	Name         string              // Service name (e.g., "ssh", "file-transfer")
	Protocol     string              // libp2p protocol ID (e.g., "/shurli/ssh/1.0.0")
	LocalAddress string              // TCP proxy target (e.g., "localhost:22"). Mutually exclusive with Handler.
	Handler      StreamHandler       // Custom stream handler for plugins. Mutually exclusive with LocalAddress.
	Enabled      bool                // Whether this service is enabled
	AllowedPeers map[peer.ID]struct{} // Per-service ACL (nil = all authorized peers allowed). Used by TCP proxy path.
	Policy       *PluginPolicy        // Transport + peer restrictions (nil = no policy, backward compat for TCP proxies).
}

// ServiceConn represents a connection to a remote service
type ServiceConn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// GrantChecker is a function that checks whether a peer has a valid
// data access grant for a given service on the given transport. Injected
// by the daemon from the grant store. When set and returns true, the
// transport is allowed for that peer+service even if the plugin policy
// doesn't allow it. This is the node-level security boundary (C2
// mitigation). Callers on the client side (pre-dial) that only want to
// know whether a usable grant exists for relay should pass TransportRelay.
type GrantChecker func(peerID peer.ID, service string, transport TransportType) bool

// TokenVerifier verifies a presented grant token (base64-encoded macaroon).
// Returns true if the token is valid for the given peer, service, and
// current stream transport. Injected by the daemon. Must do constant-time
// work on all code paths (valid, invalid, malformed) for D1 timing oracle
// mitigation.
type TokenVerifier func(tokenBase64 string, peerID peer.ID, service string, transport TransportType) bool

// TokenLookup retrieves a grant token from the GrantPouch for outbound
// presentation. Returns the base64-encoded token, or empty string if no
// token is available for the given peer and service.
type TokenLookup func(peerID peer.ID, service string) string


// ServiceRegistry manages service registration and connections.
//
// The callback fields (grantChecker, tokenVerifier, tokenLookup) follow a
// set-once-at-startup contract: they are configured via their Set* methods
// during daemon initialization, before any streams are handled. After setup,
// call Seal() to enforce the contract. The stream handler hot path reads
// callbacks without acquiring mu to avoid per-stream lock overhead. This is
// safe because the fields are never modified after Seal().
type ServiceRegistry struct {
	host          host.Host
	services      map[string]*Service
	metrics       *Metrics // nil when metrics disabled
	middleware    []StreamMiddleware
	grantChecker      GrantChecker      // set once at startup; nil = no grant checking (Phase A)
	relayGrantChecker RelayGrantChecker // set once at startup; nil = no relay grant cache check
	tokenVerifier     TokenVerifier     // set once at startup; nil = no token verification (Phase B)
	tokenLookup       TokenLookup       // set once at startup; nil = no token presentation (Phase B)
	lanRegistry       *LANRegistry      // set once at startup; nil = LAN classification uses Direct fallback
	sealed            int32             // atomic; 1 after Seal() - Set* panics if sealed
	mu                sync.RWMutex      // protects services and middleware; NOT callbacks (set-once)
}

// NewServiceRegistry creates a new service registry.
// Pass nil for metrics to disable instrumentation.
func NewServiceRegistry(h host.Host, metrics *Metrics) *ServiceRegistry {
	return &ServiceRegistry{
		host:     h,
		services: make(map[string]*Service),
		metrics:  metrics,
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

	if svc.LocalAddress == "" && svc.Handler == nil {
		return fmt.Errorf("service requires either local_address or handler")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if service already registered
	if _, exists := r.services[svc.Name]; exists {
		return fmt.Errorf("%w: %s", ErrServiceAlreadyRegistered, svc.Name)
	}

	// Register service
	r.services[svc.Name] = svc

	// Set up stream handler with middleware chain
	pid := protocol.ID(svc.Protocol)
	r.host.SetStreamHandler(pid, r.wrapWithMiddleware(svc))

	slog.Info("registered service", "service", svc.Name, "protocol", svc.Protocol, "local", svc.LocalAddress)

	return nil
}

// wrapWithMiddleware builds a libp2p stream handler for a service, wrapping
// the core handler with any registered middleware.
func (r *ServiceRegistry) wrapWithMiddleware(svc *Service) func(network.Stream) {
	// Snapshot middleware at registration time so adding middleware later
	// doesn't silently change behavior of already-registered services.
	mw := make([]StreamMiddleware, len(r.middleware))
	copy(mw, r.middleware)

	if len(mw) == 0 {
		return r.handleServiceStream(svc)
	}

	// Build the core handler as a StreamHandler.
	core := func(serviceName string, s network.Stream) {
		r.handleServiceStreamInner(svc, s)
	}

	// Apply middleware in reverse order so first-added is outermost.
	wrapped := core
	for i := len(mw) - 1; i >= 0; i-- {
		wrapped = mw[i](wrapped)
	}

	return func(s network.Stream) {
		wrapped(svc.Name, s)
	}
}

// handleServiceStream creates a stream handler for a service (no middleware)
func (r *ServiceRegistry) handleServiceStream(svc *Service) func(network.Stream) {
	return func(s network.Stream) {
		r.handleServiceStreamInner(svc, s)
	}
}

// handleServiceStreamInner is the core stream handler logic, shared by both
// the direct handler and the middleware-wrapped handler.
func (r *ServiceRegistry) handleServiceStreamInner(svc *Service, s network.Stream) {
	remotePeer := s.Conn().RemotePeer()
	tag := connectionTag(s)
	short := remotePeer.String()[:16] + "..."
	slog.Info("incoming connection", "path", tag, "service", svc.Name, "peer", short)

	// Phase B: read grant header on plugin services.
	// The remote side (OpenPluginStream) always writes a header for plugin streams.
	var presentedToken string
	if svc.Policy != nil {
		var err error
		presentedToken, err = ReadGrantHeader(s)
		if err != nil {
			slog.Warn("grant header read failed", "service", svc.Name, "peer", short, "error", err)
			s.Reset()
			return
		}
	}

	// Plugin policy enforcement (transport + peer restrictions).
	//
	// VerifiedTransport (not ClassifyTransport) is used so that routed-private
	// IPv4 addresses (Starlink CGNAT, Docker, VPN, multi-WAN WAN2 cross-link)
	// correctly classify as Direct, not LAN. Only mDNS-verified connections
	// count as LAN for policy decisions.
	if svc.Policy != nil {
		transport := VerifiedTransport(s, func(pid peer.ID) bool {
			if r.lanRegistry == nil {
				return false
			}
			return r.lanRegistry.HasVerifiedLANConn(r.host, pid)
		})
		if !svc.Policy.TransportAllowed(transport) {
			// Grant override: a transport-caveated grant (or plain grant under
			// Phase A) can unlock any transport the plugin policy restricts,
			// not just relay. The verifier / grant store checks the grant's
			// own transport caveat against the current stream transport; an
			// unrestricted grant unlocks all, a narrowed grant unlocks only
			// the transports it names.
			//
			// D1 timing: when a token is presented, ONLY use the token path
			// (tokenVerifier does constant-time HMAC on all outcomes). When no
			// token, fall back to grantChecker (Phase A, has its own D1 dummy).
			// Never run both - that creates a timing distinguisher.
			granted := false
			if presentedToken != "" && r.tokenVerifier != nil {
				granted = r.tokenVerifier(presentedToken, remotePeer, svc.Name, transport)
				if granted {
					slog.Info("plugin transport allowed via presented token",
						"service", svc.Name, "peer", short, "transport", transport)
				}
			} else if r.grantChecker != nil {
				granted = r.grantChecker(remotePeer, svc.Name, transport)
				if granted {
					slog.Info("plugin transport allowed via grant store",
						"service", svc.Name, "peer", short, "transport", transport)
				}
			}
			if !granted {
				slog.Warn("plugin transport not allowed",
					"service", svc.Name, "peer", short,
					"transport", transport, "allowed", svc.Policy.AllowedTransports,
					"token_presented", presentedToken != "")
				s.Reset()
				return
			}
		}
		if !svc.Policy.PeerAllowed(remotePeer) {
			slog.Warn("peer denied by plugin policy", "service", svc.Name, "peer", short)
			s.Reset()
			return
		}
	}

	// Per-service access control (legacy path for TCP proxies without Policy).
	if svc.Policy == nil && svc.AllowedPeers != nil {
		if _, ok := svc.AllowedPeers[remotePeer]; !ok {
			slog.Warn("peer not in service ACL", "service", svc.Name, "peer", short)
			s.Reset()
			return
		}
	}

	// Custom handler path: delegate to the plugin's stream handler.
	if svc.Handler != nil {
		svc.Handler(svc.Name, s)
		return
	}

	// TCP proxy path: connect to local service and proxy bidirectionally.
	localConn, err := net.DialTimeout("tcp", svc.LocalAddress, 10*time.Second)
	if err != nil {
		slog.Error("failed to connect to local service", "service", svc.Name, "addr", svc.LocalAddress, "error", err)
		s.Reset()
		return
	}

	// Bidirectional proxy with half-close propagation and optional metrics
	InstrumentedBidirectionalProxy(&serviceStream{stream: s}, &tcpHalfCloser{localConn}, svc.Name, r.metrics)

	slog.Info("closed connection", "service", svc.Name, "peer", short)
}

// DialService connects to a remote peer's service.
// If the protocol matches a locally registered service with a PluginPolicy,
// the policy's transport restrictions are enforced.
func (r *ServiceRegistry) DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error) {
	pid := protocol.ID(protocolID)

	slog.Info("dialing service", "peer", peerID.String()[:16]+"...", "protocol", protocolID)

	// Look up local registration to check policy.
	var policy *PluginPolicy
	r.mu.RLock()
	for _, svc := range r.services {
		if svc.Protocol == protocolID {
			policy = svc.Policy
			break
		}
	}
	r.mu.RUnlock()

	// Respect transport policy: only allow relay if policy permits.
	dialCtx := ctx
	if policy == nil || policy.RelayAllowed() {
		dialCtx = network.WithAllowLimitedConn(ctx, protocolID)
	}

	// Open stream to remote peer
	s, err := r.host.NewStream(dialCtx, peerID, pid)
	if err != nil {
		// UX hint: if the peer is only reachable through relay circuits and
		// the stream failed, the relay likely blocked the data circuit.
		if isRelayOnlyPeer(r.host, peerID) {
			if policy != nil && !policy.RelayAllowed() {
				return nil, fmt.Errorf("plugin policy does not allow relay, and peer is only reachable via relay: %w", err)
			}
			return nil, fmt.Errorf("failed to open stream: %w\n\n%s", err, relayDataHint)
		}
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	tag := connectionTag(s)
	slog.Info("connected to peer", "path", tag, "peer", peerID.String()[:16]+"...", "protocol", protocolID)

	return &serviceStream{stream: s}, nil
}

// isRelayOnlyPeer checks if the only active connections to a peer are
// limited (relay circuit) connections. Returns false if no connections exist
// or if any direct connection is present.
func isRelayOnlyPeer(h host.Host, p peer.ID) bool {
	conns := h.Network().ConnsToPeer(p)
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		if !c.Stat().Limited {
			return false // has a direct connection
		}
	}
	return true
}

// peerRelayFromConns returns the relay peer ID from the first limited (relay)
// connection to the given peer. Returns empty if no relay connections exist.
func peerRelayFromConns(h host.Host, p peer.ID) peer.ID {
	for _, c := range h.Network().ConnsToPeer(p) {
		if c.Stat().Limited {
			rid := RelayPeerFromAddr(c.RemoteMultiaddr())
			if rid != "" {
				return rid
			}
		}
	}
	return ""
}

// hasAnyActiveRelayGrant checks if any relay the peer is connected through
// has an active grant receipt in our cache. This enables outbound plugin
// streams to use relay transport when a relay admin has granted data access.
func hasAnyActiveRelayGrant(checker RelayGrantChecker, h host.Host, peerID peer.ID) bool {
	// Find relay peer IDs from the peer's limited (relay) connections.
	conns := h.Network().ConnsToPeer(peerID)
	for _, c := range conns {
		if !c.Stat().Limited {
			continue // direct connection, not relayed
		}
		rid := RelayPeerFromAddr(c.RemoteMultiaddr())
		if rid != "" {
			if _, _, _, ok := checker.GrantStatus(rid); ok {
				return true
			}
		}
	}
	// Also check peerstore addresses if no active connections exist.
	// The dial will establish a relay connection; the relay enforces the grant.
	// Post-dial verification re-checks the actual connection's relay.
	if len(conns) == 0 {
		for _, addr := range h.Peerstore().Addrs(peerID) {
			rid := RelayPeerFromAddr(addr)
			if rid != "" {
				if _, _, _, ok := checker.GrantStatus(rid); ok {
					return true
				}
			}
		}
	}
	return false
}

const relayDataHint = `This relay is a discovery node, NOT a full data relay.
It enables peer discovery and direct connections only.
No SSH, XRDP, or other data is forwarded through it.

To transfer data between your devices:
  1. Both peers must connect directly (check firewall/NAT settings)
  2. Or deploy your own relay for full data relay: https://shurli.io/docs/relay-setup/
  3. Or ask the relay admin: shurli relay grant <your-peer-id> --duration 1h`

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

// Seal marks the registry as fully configured. After Seal(), calling any
// Set* method panics. Call this after daemon initialization completes,
// before streams are handled. This enforces the set-once-at-startup contract.
func (r *ServiceRegistry) Seal() {
	atomic.StoreInt32(&r.sealed, 1)
}

// SetGrantChecker sets the grant checker function used for relay authorization.
// When a peer has a valid grant, relay transport is allowed for that peer+service
// even if the plugin's default policy is LAN+Direct only.
// Must be called before Seal().
func (r *ServiceRegistry) SetGrantChecker(checker GrantChecker) {
	if atomic.LoadInt32(&r.sealed) != 0 {
		panic("ServiceRegistry: SetGrantChecker called after Seal()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grantChecker = checker
}

// SetRelayGrantChecker sets the relay grant cache checker for outbound streams.
// When set and any relay has an active grant receipt, relay transport is allowed
// for outbound plugin streams. Bridges relay-side grants to client-side policy.
// Must be called before Seal().
func (r *ServiceRegistry) SetRelayGrantChecker(checker RelayGrantChecker) {
	if atomic.LoadInt32(&r.sealed) != 0 {
		panic("ServiceRegistry: SetRelayGrantChecker called after Seal()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relayGrantChecker = checker
}

// SetTokenVerifier sets the function used to verify presented grant tokens
// on inbound plugin streams (Phase B). The verifier decodes the base64 token
// and checks the macaroon HMAC chain + caveats.
// Must be called before Seal().
func (r *ServiceRegistry) SetTokenVerifier(v TokenVerifier) {
	if atomic.LoadInt32(&r.sealed) != 0 {
		panic("ServiceRegistry: SetTokenVerifier called after Seal()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenVerifier = v
}

// SetTokenLookup sets the function used to retrieve grant tokens from the
// GrantPouch for outbound plugin streams (Phase B).
// Must be called before Seal().
func (r *ServiceRegistry) SetTokenLookup(l TokenLookup) {
	if atomic.LoadInt32(&r.sealed) != 0 {
		panic("ServiceRegistry: SetTokenLookup called after Seal()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenLookup = l
}

// SetLANRegistry wires the mDNS-verified LAN registry so that plugin-policy
// transport classification uses verified-LAN detection instead of bare
// RFC 1918 matching. Routed-private IPs (Starlink CGNAT, Docker, VPN, or
// multi-WAN routed-private subnets) correctly classify as Direct when the
// peer has no mDNS-verified connection. Must be called before Seal().
func (r *ServiceRegistry) SetLANRegistry(lanReg *LANRegistry) {
	if atomic.LoadInt32(&r.sealed) != 0 {
		panic("ServiceRegistry: SetLANRegistry called after Seal()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lanRegistry = lanReg
}

// Use adds stream middleware that wraps every inbound stream handler.
// Middleware is applied in the order added (first added = outermost wrapper).
func (r *ServiceRegistry) Use(middleware ...StreamMiddleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, middleware...)
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
