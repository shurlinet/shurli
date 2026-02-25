package main

import (
	"context"
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

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/invite"
	"github.com/shurlinet/shurli/internal/qr"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

const inviteProtocol = "/shurli/invite/1.0.0"

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
		fatal("Config error: %v\nRun 'shurli init' first.", err)
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
		UserAgent:          "shurli/" + version,
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

	// Encode invite (v1 PAKE format with namespace)
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

		fmt.Println("Or on that device, run:  shurli join <code>")
	}
	outln()
	outln("Waiting for peer to join...")

	// Set up invite protocol handler (PAKE only)
	joined := make(chan string, 1) // receives joiner's name

	h.SetStreamHandler(protocol.ID(inviteProtocol), func(s network.Stream) {
		defer s.Close()
		remotePeer := s.Conn().RemotePeer()

		// Read version byte. PAKE starts with 0x01.
		var firstByte [1]byte
		if _, err := io.ReadFull(s, firstByte[:]); err != nil {
			log.Printf("Invite stream read error: %v", err)
			return
		}

		if firstByte[0] != invite.VersionV1 {
			log.Printf("Unknown invite protocol version 0x%02x from %s", firstByte[0], remotePeer.String()[:16])
			return
		}

		joinerName, success := handleInvite(s, token, *nameFlag, remotePeer.String(), cfg.Security.AuthorizedKeysFile)
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

// handleInvite processes a PAKE handshake on the invite stream.
// The version byte has already been read by the caller.
// Returns the joiner's name and whether authorization succeeded.
func handleInvite(s network.Stream, token [8]byte, inviterName, remotePeerStr, authKeysPath string) (string, bool) {
	// Read joiner's X25519 public key (32 bytes follow the version byte)
	joinerPub, err := invite.ReadPublicKey(s)
	if err != nil {
		log.Printf("PAKE: failed to read joiner public key from %s: %v", remotePeerStr[:16], err)
		return "", false
	}

	// Create our PAKE session and send our public key
	session, err := invite.NewPAKESession()
	if err != nil {
		log.Printf("PAKE: session error: %v", err)
		return "", false
	}

	if err := session.WritePublicKey(s); err != nil {
		log.Printf("PAKE: failed to send public key: %v", err)
		return "", false
	}

	// Complete DH exchange with token binding
	if err := session.Complete(joinerPub, token); err != nil {
		log.Printf("PAKE: key exchange failed from %s: %v", remotePeerStr[:16], err)
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
		log.Printf("PAKE: failed to send response: %v", err)
		return "", false
	}

	// Allow the response to flush through the relay circuit
	time.Sleep(2 * time.Second)

	return joinerName, true
}
