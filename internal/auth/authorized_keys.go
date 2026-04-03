package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// LoadAuthorizedKeys loads and parses an authorized_keys file.
// Returns a simple peer ID -> bool map for backward compatibility.
// Format: <peer-id> [key=value attrs...] [# comment]
func LoadAuthorizedKeys(path string) (map[peer.ID]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open authorized_keys file: %w", err)
	}
	defer file.Close()

	authorizedPeers := make(map[peer.ID]bool)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		peerIDStr, _, _ := parseLine(scanner.Text())
		if peerIDStr == "" {
			continue
		}

		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			return nil, fmt.Errorf("invalid peer ID at line %d: %s (error: %w)", lineNum, peerIDStr, err)
		}

		authorizedPeers[peerID] = true
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading authorized_keys file: %w", err)
	}

	return authorizedPeers, nil
}

// IsAuthorized checks if a peer ID is in the authorized list
func IsAuthorized(peerID peer.ID, authorizedPeers map[peer.ID]bool) bool {
	return authorizedPeers[peerID]
}

// LoadCommentMap loads an authorized_keys file and returns a map from
// comment (lowercased) to peer ID. Only entries with non-empty comments
// are included. This enables resolving friendly names like "relay" or
// "home-node" to their peer IDs.
func LoadCommentMap(path string) (map[string]peer.ID, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := make(map[string]peer.ID)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		peerIDStr, _, comment := parseLine(scanner.Text())
		if peerIDStr == "" || comment == "" {
			continue
		}
		pid, err := peer.Decode(peerIDStr)
		if err != nil {
			continue
		}
		result[strings.ToLower(comment)] = pid
	}
	return result, scanner.Err()
}

