package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/invite"
	"github.com/satindergrewal/peer-up/internal/qr"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

const inviteProtocol = "/peerup/invite/1.0.0"

func runInvite(args []string) {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	nameFlag := fs.String("name", "", "friendly name for this peer (e.g., \"home\")")
	ttlFlag := fs.Duration("ttl", 10*time.Minute, "invite code expiry duration")
	nonInteractive := fs.Bool("non-interactive", false, "machine-friendly output (no QR, bare code to stdout)")
	fs.Parse(args)

	// In non-interactive mode, progress goes to stderr so stdout has only the invite code.
	out := fmt.Printf
	outln := fmt.Println
	if *nonInteractive {
		out = func(format string, a ...any) (int, error) { return fmt.Fprintf(os.Stderr, format, a...) }
		outln = func(a ...any) (int, error) { return fmt.Fprintln(os.Stderr, a...) }
	}

	// Load config
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fatal("Config error: %v\nRun 'peerup init' first.", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if len(cfg.Relay.Addresses) == 0 {
		fatal("No relay addresses in config. Cannot create invite.")
	}

	// Generate token
	token, err := invite.GenerateToken()
	if err != nil {
		fatal("Failed to generate token: %v", err)
	}

	// Create P2P network (no connection gating - we need the joiner to reach us)
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "peerup/" + version,
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		fatal("P2P network error: %v", err)
	}
	defer p2pNetwork.Close()

	h := p2pNetwork.Host()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to relay
	relayInfos, err := p2pnet.ParseRelayAddrs(cfg.Relay.Addresses)
	if err != nil {
		fatal("Failed to parse relay addresses: %v", err)
	}
	for _, ai := range relayInfos {
		if err := h.Connect(ctx, ai); err != nil {
			fatal("Failed to connect to relay: %v", err)
		}
	}

	// Make explicit relay reservation (AutoRelay alone is unreliable for short-lived commands)
	outln("Waiting for relay reservation...")
	time.Sleep(2 * time.Second) // allow AutoRelay a chance first

	hasRelay := false
	for _, addr := range h.Addrs() {
		if strings.Contains(addr.String(), "p2p-circuit") {
			hasRelay = true
			break
		}
	}
	if !hasRelay {
		for _, ai := range relayInfos {
			if _, err := circuitv2client.Reserve(ctx, h, ai); err != nil {
				fatal("Relay reservation failed: %v", err)
			}
		}
	}

	// Encode invite (v2 format with namespace)
	inviteData := &invite.InviteData{
		Token:     token,
		RelayAddr: cfg.Relay.Addresses[0],
		PeerID:    h.ID(),
		Network:   cfg.Discovery.Network,
	}
	code, err := invite.Encode(inviteData)
	if err != nil {
		fatal("Failed to encode invite: %v", err)
	}

	if *nonInteractive {
		// Bare code to stdout for piping/scripting
		fmt.Println(code)
	} else {
		fmt.Println()
		fmt.Printf("=== Invite Code (expires in %s) ===\n", *ttlFlag)
		fmt.Println()
		fmt.Println(code)
		fmt.Println()

		// Show QR code for easy scanning (e.g., from mobile app)
		q, err := qr.New(code, qr.Medium)
		if err == nil {
			fmt.Println("Scan this QR code to join:")
			fmt.Println()
			fmt.Print(q.ToSmallString(false))
		}

		fmt.Println("Or on that device, run:  peerup join <code>")
	}
	outln()
	outln("Waiting for peer to join...")

	// Set up invite protocol handler (supports both v1 and v2)
	joined := make(chan string, 1) // receives joiner's name

	h.SetStreamHandler(protocol.ID(inviteProtocol), func(s network.Stream) {
		defer s.Close()
		remotePeer := s.Conn().RemotePeer()

		// Peek at first byte to determine protocol version.
		// v2 starts with 0x02 (PAKE handshake), v1 starts with ASCII hex (0x30-0x66).
		var firstByte [1]byte
		if _, err := io.ReadFull(s, firstByte[:]); err != nil {
			log.Printf("Invite stream read error: %v", err)
			return
		}

		var joinerName string
		var success bool

		if firstByte[0] == invite.VersionV2 {
			joinerName, success = handleInviteV2(s, token, *nameFlag, remotePeer.String(), cfg.Security.AuthorizedKeysFile)
		} else {
			joinerName, success = handleInviteV1(s, firstByte[0], token, *nameFlag, remotePeer.String(), cfg.Security.AuthorizedKeysFile)
		}

		if success {
			joined <- joinerName
		}
	})

	// Wait for join or timeout/interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	timer := time.NewTimer(*ttlFlag)

	select {
	case name := <-joined:
		timer.Stop()
		outln()
		if name != "" {
			out("Peer \"%s\" joined and authorized!\n", name)
		} else {
			outln("Peer joined and authorized!")
		}
		out("Authorized keys file: %s\n", cfg.Security.AuthorizedKeysFile)
	case <-timer.C:
		outln()
		outln("Invite expired. No peer joined.")
	case <-sigCh:
		outln("\nCancelled.")
	}
}

// handleInviteV2 processes a v2 PAKE handshake on the invite stream.
// Returns the joiner's name and whether authorization succeeded.
func handleInviteV2(s network.Stream, token [8]byte, inviterName, remotePeerStr, authKeysPath string) (string, bool) {
	// Read joiner's X25519 public key (32 bytes follow the version byte)
	joinerPub, err := invite.ReadPublicKey(s)
	if err != nil {
		log.Printf("v2: failed to read joiner public key from %s: %v", remotePeerStr[:16], err)
		return "", false
	}

	// Create our PAKE session and send our public key
	session, err := invite.NewPAKESession()
	if err != nil {
		log.Printf("v2: PAKE session error: %v", err)
		return "", false
	}

	if err := session.WritePublicKey(s); err != nil {
		log.Printf("v2: failed to send public key: %v", err)
		return "", false
	}

	// Complete DH exchange with token binding
	if err := session.Complete(joinerPub, token); err != nil {
		log.Printf("v2: PAKE key exchange failed from %s: %v", remotePeerStr[:16], err)
		return "", false
	}

	// Read encrypted joiner name
	joinerNameBytes, err := session.Decrypt(s)
	if err != nil {
		// Token mismatch causes AEAD decryption failure.
		// Log as "invalid invite code" without leaking protocol details.
		log.Printf("Invalid invite code from %s", remotePeerStr[:16])
		return "", false
	}
	joinerName := string(joinerNameBytes)

	// Authorize the joiner
	comment := joinerName
	if comment == "" {
		comment = "joined-" + time.Now().Format("2006-01-02")
	}
	remotePeer := s.Conn().RemotePeer()
	if err := auth.AddPeer(authKeysPath, remotePeer.String(), comment); err != nil {
		if !strings.Contains(err.Error(), "already authorized") {
			log.Printf("Failed to authorize peer: %v", err)
			session.WriteEncrypted(s, []byte("ERR server error"))
			return "", false
		}
	}

	// Send encrypted response: "OK <inviter_name>"
	response := "OK " + inviterName
	if err := session.WriteEncrypted(s, []byte(response)); err != nil {
		log.Printf("v2: failed to send response: %v", err)
		return "", false
	}

	// Allow the response to flush through the relay circuit
	time.Sleep(2 * time.Second)

	return joinerName, true
}

// handleInviteV1 processes a v1 cleartext handshake on the invite stream.
// The first byte has already been read (it's the first char of the token hex).
// Returns the joiner's name and whether authorization succeeded.
func handleInviteV1(s network.Stream, firstByte byte, token [8]byte, inviterName, remotePeerStr, authKeysPath string) (string, bool) {
	// v1: first byte is part of the hex token. Read the rest of the line.
	scanner := bufio.NewScanner(s)
	scanner.Buffer(make([]byte, 512), 512)
	if !scanner.Scan() {
		log.Printf("Invite stream read error: %v", scanner.Err())
		s.Write([]byte("ERR read error\n"))
		return "", false
	}
	// Prepend the first byte we already read
	line := string(firstByte) + scanner.Text()
	parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
	receivedToken := parts[0]
	joinerName := ""
	if len(parts) > 1 {
		joinerName = parts[1]
	}

	// Verify token
	expectedHex := hex.EncodeToString(token[:])
	if receivedToken != expectedHex {
		log.Printf("Invalid token from %s", remotePeerStr[:16])
		s.Write([]byte("ERR invalid token\n"))
		return "", false
	}

	// Add joiner to authorized_keys
	comment := joinerName
	if comment == "" {
		comment = "joined-" + time.Now().Format("2006-01-02")
	}
	remotePeer := s.Conn().RemotePeer()
	if err := auth.AddPeer(authKeysPath, remotePeer.String(), comment); err != nil {
		if !strings.Contains(err.Error(), "already authorized") {
			log.Printf("Failed to authorize peer: %v", err)
			s.Write([]byte("ERR server error\n"))
			return "", false
		}
	}

	// Send confirmation: OK <inviter_name>\n
	s.Write([]byte(fmt.Sprintf("OK %s\n", inviterName)))

	// Allow the response to flush through the relay circuit
	time.Sleep(2 * time.Second)

	return joinerName, true
}
