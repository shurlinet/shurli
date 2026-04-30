package sdk

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// ServiceQueryProtocol is the protocol ID for querying a peer's services.
const ServiceQueryProtocol = "/shurli/service-query/1.0.0"

// Wire message types for service query.
const (
	serviceQueryVersion     = 0x01
	msgServiceQueryRequest  = 0x01
	msgServiceQueryResponse = 0x02
	msgServiceQueryError    = 0x03
)

const serviceQueryTimeout = 10 * time.Second

// RemoteServiceInfo is the public-safe subset of a service returned to remote peers.
// LocalAddress is deliberately omitted (security: never expose internal topology).
type RemoteServiceInfo struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
}

// HandleServiceQuery returns a stream handler that responds with this node's
// enabled services. Only service name and protocol are exposed. Local addresses
// are never sent to remote peers.
func HandleServiceQuery(registry *ServiceRegistry) StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()

		s.SetDeadline(time.Now().Add(serviceQueryTimeout))

		// Read version + request type.
		var reqHeader [2]byte
		if _, err := io.ReadFull(s, reqHeader[:]); err != nil {
			return
		}

		if reqHeader[0] != serviceQueryVersion {
			writeServiceQueryError(s, "unsupported version")
			return
		}
		if reqHeader[1] != msgServiceQueryRequest {
			writeServiceQueryError(s, "unknown request type")
			return
		}

		// Collect enabled services.
		services := registry.ListServices()
		var infos []RemoteServiceInfo
		for _, svc := range services {
			if !svc.Enabled {
				continue
			}
			infos = append(infos, RemoteServiceInfo{
				Name:     svc.Name,
				Protocol: svc.Protocol,
				Enabled:  true,
			})
		}

		data, err := json.Marshal(infos)
		if err != nil {
			slog.Warn("service-query: marshal failed", "err", err)
			writeServiceQueryError(s, "internal error")
			return
		}

		// Write response: type(1) + length(4) + data.
		var header [5]byte
		header[0] = msgServiceQueryResponse
		binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
		if _, err := s.Write(header[:]); err != nil {
			return
		}
		if _, err := s.Write(data); err != nil {
			slog.Debug("service-query: write response data failed", "err", err)
		}
	}
}

// QueryPeerServices queries a remote peer's services via the service-query protocol.
func QueryPeerServices(s network.Stream) ([]RemoteServiceInfo, error) {
	s.SetDeadline(time.Now().Add(serviceQueryTimeout))

	// Send version + request type.
	var reqBuf [2]byte
	reqBuf[0] = serviceQueryVersion
	reqBuf[1] = msgServiceQueryRequest
	if _, err := s.Write(reqBuf[:]); err != nil {
		return nil, err
	}

	// Read response header: type(1) + length(4).
	var header [5]byte
	if _, err := io.ReadFull(s, header[:]); err != nil {
		return nil, err
	}

	respType := header[0]
	dataLen := binary.BigEndian.Uint32(header[1:])

	if dataLen > 1<<20 { // 1 MB sanity limit
		return nil, ErrResponseTooLarge
	}

	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, err
	}

	if respType == msgServiceQueryError {
		return nil, &RemoteError{Message: string(data)}
	}

	var infos []RemoteServiceInfo
	if err := json.Unmarshal(data, &infos); err != nil {
		return nil, err
	}

	return infos, nil
}

func writeServiceQueryError(s network.Stream, msg string) {
	data := []byte(msg)
	var header [5]byte
	header[0] = msgServiceQueryError
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return
	}
	if _, err := s.Write(data); err != nil {
		slog.Debug("service-query: write error response failed", "err", err)
	}
}
