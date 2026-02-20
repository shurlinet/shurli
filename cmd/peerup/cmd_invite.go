package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

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

	// Create P2P network (no connection gating  - we need the joiner to reach us)
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

	// Wait for relay address
	outln("Waiting for relay reservation...")
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}

	// Encode invite
	inviteData := &invite.InviteData{
		Token:     token,
		RelayAddr: cfg.Relay.Addresses[0],
		PeerID:    h.ID(),
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

	// Set up invite protocol handler
	joined := make(chan string, 1) // receives joiner's name

	h.SetStreamHandler(protocol.ID(inviteProtocol), func(s network.Stream) {
		defer s.Close()
		remotePeer := s.Conn().RemotePeer()

		// Read: <token_hex> <joiner_name>\n
		// Limit read to 512 bytes to prevent OOM from a malicious peer
		// sending unbounded data without a newline.
		scanner := bufio.NewScanner(s)
		scanner.Buffer(make([]byte, 512), 512)
		if !scanner.Scan() {
			log.Printf("Invite stream read error: %v", scanner.Err())
			s.Write([]byte("ERR read error\n"))
			return
		}
		line := scanner.Text()
		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		receivedToken := parts[0]
		joinerName := ""
		if len(parts) > 1 {
			joinerName = parts[1]
		}

		// Verify token
		expectedHex := hex.EncodeToString(token[:])
		if receivedToken != expectedHex {
			log.Printf("Invalid token from %s", remotePeer.String()[:16])
			s.Write([]byte("ERR invalid token\n"))
			return
		}

		// Add joiner to authorized_keys
		comment := joinerName
		if comment == "" {
			comment = "joined-" + time.Now().Format("2006-01-02")
		}
		authKeysPath := cfg.Security.AuthorizedKeysFile
		if err := auth.AddPeer(authKeysPath, remotePeer.String(), comment); err != nil {
			if strings.Contains(err.Error(), "already authorized") {
				log.Printf("Peer already authorized: %s", remotePeer.String()[:16])
			} else {
				log.Printf("Failed to authorize peer: %v", err)
				s.Write([]byte("ERR server error\n"))
				return
			}
		}

		// Send confirmation: OK <inviter_name>\n
		inviterName := *nameFlag
		s.Write([]byte(fmt.Sprintf("OK %s\n", inviterName)))

		// Allow the response to flush through the relay circuit before
		// the deferred s.Close() fires and the host potentially shuts down.
		time.Sleep(2 * time.Second)

		joined <- joinerName
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
