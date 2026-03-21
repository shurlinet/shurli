package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shurlinet/shurli/pkg/plugin"
)

// manInstallDir returns the man page directory for section 1.
// User-local by default (no sudo needed). System path when running as root.
func manInstallDir() string {
	if os.Getuid() == 0 {
		return "/usr/local/share/man/man1"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "man", "man1")
}

func manInstallPath() string {
	return filepath.Join(manInstallDir(), "shurli.1")
}

func runMan() {
	// Check for --install / --uninstall flags.
	args := os.Args[2:]
	if len(args) > 0 {
		switch args[0] {
		case "--install":
			installManPage()
			return
		case "--uninstall":
			uninstallManPage()
			return
		case "--path":
			fmt.Println(manInstallPath())
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[0])
			fmt.Fprintln(os.Stderr, "Usage: shurli man [--install|--uninstall|--path]")
			osExit(1)
		}
	}

	// Default: display the man page.
	displayManPage()
}

func displayManPage() {
	content := manPage()

	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, "shurli.1")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		fmt.Print(content)
		return
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command("man", tmpFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Print(content)
	}
}

func installManPage() {
	dir := manInstallDir()
	dest := manInstallPath()

	// Create directory if it does not exist.
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", dir, err)
		if os.IsPermission(err) {
			fmt.Fprintln(os.Stderr, "Try: sudo shurli man --install")
		}
		osExit(1)
	}

	content := manPage()
	if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", dest, err)
		if os.IsPermission(err) {
			fmt.Fprintln(os.Stderr, "Try: sudo shurli man --install")
		}
		osExit(1)
	}

	// On Linux, update the man page database index.
	if runtime.GOOS == "linux" {
		if path, err := exec.LookPath("mandb"); err == nil {
			exec.Command(path, "--quiet").Run()
		}
	}

	fmt.Printf("Installed: %s\n", dest)
	if os.Getuid() != 0 {
		fmt.Printf("Ensure MANPATH includes %s. Add to your shell profile if needed:\n", dir)
		fmt.Printf("  export MANPATH=\"%s:$MANPATH\"\n", filepath.Dir(dir))
	}
	fmt.Println("You can now run: man shurli")
}

func uninstallManPage() {
	dest := manInstallPath()

	if _, err := os.Stat(dest); os.IsNotExist(err) {
		fmt.Println("Man page not installed.")
		return
	}

	if err := os.Remove(dest); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove %s: %v\n", dest, err)
		if os.IsPermission(err) {
			fmt.Fprintln(os.Stderr, "Try: sudo shurli man --uninstall")
		}
		osExit(1)
	}

	fmt.Printf("Removed: %s\n", dest)
}

func manPage() string {
	page := `.TH SHURLI 1 "2026-03-02" "shurli ` + version + `" "Shurli Manual"

.SH NAME
shurli \- sovereign peer-to-peer networking tool

.SH SYNOPSIS
.B shurli
.I command
.RI [ options ]
.br
.B shurli daemon
.br
.B shurli invite
[\fB--as\fR \fIname\fR]
.br
.B shurli join
.I code
[\fB--as\fR \fIname\fR]
.br
.B shurli proxy
.I target service local-port

.SH DESCRIPTION
.B shurli
creates encrypted peer-to-peer tunnels between your devices. It uses
.BR libp2p (7)
for transport, requiring no cloud accounts, no port forwarding, and no DNS.
Every connection is authenticated by cryptographic identity and encrypted
end-to-end.
.PP
The typical deployment has three actors: a
.B relay server
running on a VPS (the only component with a public IP), and two or more
.B nodes
(your devices) that connect through it. The relay cannot read your traffic;
it only forwards opaque encrypted streams between authorized peers.
.PP
Shurli discovers the fastest path automatically:
.IP 1. 4
.B LAN
\- if peers are on the same local network, mDNS discovery connects them
directly with sub-millisecond latency. No relay involved.
.IP 2. 4
.B Direct IPv6
\- if both peers have global IPv6 addresses, shurli probes a direct
connection. Works across ISPs with no relay overhead.
.IP 3. 4
.B Relay
\- when NAT, CGNAT, or firewalls block direct connections, traffic flows
through your relay server. This is the fallback, not the default.
.PP
All three paths are encrypted identically. The transition between them is
automatic and invisible to the user.

.SH GETTING STARTED
A minimal setup takes about two minutes:
.PP
.B "On your first device:"
.nf
  shurli init
  shurli daemon
.fi
.PP
.B "On your second device:"
.nf
  shurli init
.fi
.PP
.B "Pair them (from either device):"
.nf
  shurli invite --as home
.fi
.PP
This prints a one-time code. On the other device:
.nf
  shurli join <code> --as laptop
.fi
.PP
Both devices are now authorized and can reach each other.

.SH EXAMPLES
.SS Expose SSH and connect from another device
.nf
  # On the server (home machine):
  shurli service add ssh 127.0.0.1:22
  shurli daemon

  # On the client (laptop):
  shurli proxy home ssh 2222
  ssh -p 2222 user@127.0.0.1
.fi
.PP
The proxy command binds 127.0.0.1:2222 on your laptop. SSH traffic flows
through the encrypted P2P tunnel to port 22 on your home machine. The
\fBhome\fR name is resolved from the names section of your config.

.SS Ping a peer by name
.nf
  shurli ping home -c 5
.fi
.PP
Sends 5 P2P pings. Reports round-trip time, connection type (LAN/direct/relay),
and packet loss.

.SS Forward any TCP service
.nf
  # Expose a Grafana dashboard:
  shurli service add grafana 127.0.0.1:3000

  # Access it from another device:
  shurli proxy home grafana 3000
  # Open http://127.0.0.1:3000 in your browser
.fi

.SS Set up a relay server on a VPS
.nf
  # On the VPS:
  shurli relay setup
  shurli relay serve

  # Generate an invite code:
  shurli relay invite create --ttl 24h

  # On each device, join with the code:
  shurli init
  # (enter the relay address when prompted)
.fi

.SS Verify a peer's identity
.nf
  shurli verify laptop
.fi
.PP
Both devices display a 4-emoji fingerprint. Compare them out-of-band
(in person, phone call, etc.). Verified peers get a permanent trust
marker; unverified peers show an [UNVERIFIED] badge.

.SS Safe config changes with auto-revert
.nf
  shurli config apply new-config.yaml --confirm-timeout 5m
  # Test that everything works...
  shurli config confirm
.fi
.PP
If you do not confirm within 5 minutes, the previous config is restored
automatically. Useful for remote machines where a bad config could lock
you out.

.\" ===================================================================
.\" COMMAND REFERENCE
.\" ===================================================================

.SH DAEMON COMMANDS
The daemon runs the P2P host, listens for incoming connections, and exposes
a local control API over a Unix socket.
.TP
.B daemon
Start the daemon in the foreground.
.TP
.B daemon status \fR[\fB--json\fR]
Query the running daemon for its peer ID, uptime, connected peers, and
active proxies.
.TP
.B daemon stop
Send a graceful shutdown signal. Active proxy tunnels are drained before exit.
.TP
.B daemon ping \fItarget\fR [\fB-c\fR \fIN\fR] [\fB--interval\fR \fIms\fR] [\fB--json\fR]
Ping a peer through the daemon. The target can be a peer ID or a friendly
name from your config. Default: 4 pings at 1-second intervals.
.TP
.B daemon services \fR[\fB--json\fR]
List services registered with the daemon (both local and remote).
.TP
.B daemon peers \fR[\fB--all\fR] [\fB--json\fR]
List connected peers. By default, shows only authorized peers. Use
\fB--all\fR to include DHT routing table neighbors.
.TP
.B daemon paths \fR[\fB--json\fR]
Show the current connection path for each peer: LAN, direct, or relayed.
Includes latency and the relay address if applicable.
.TP
.B daemon connect \fB--peer\fR \fIname\fR \fB--service\fR \fIname\fR \fB--listen\fR \fIaddr\fR
Open a persistent TCP proxy through the daemon. Survives brief disconnections.
.TP
.B daemon disconnect \fIid\fR
Tear down a proxy tunnel by its ID (shown in \fBdaemon status\fR output).

.SH NETWORK TOOLS
These commands create a temporary P2P host, perform their operation, and exit.
They do not require a running daemon. Useful for quick diagnostics.
.TP
.B ping \fItarget\fR [\fB-c\fR \fIN\fR] [\fB--interval\fR \fI1s\fR] [\fB--json\fR]
P2P ping. Measures round-trip time over the encrypted tunnel. With \fB-c 0\fR,
pings continuously until interrupted.
.TP
.B traceroute \fItarget\fR [\fB--json\fR]
Trace the P2P path to a peer. Shows whether the connection is direct or relayed,
and the relay hops involved.
.TP
.B resolve \fIname\fR [\fB--json\fR]
Look up a friendly name in your config and resolve it to a peer ID. Also queries
the DHT if the name is not found locally.
.TP
.B proxy \fItarget\fR \fIservice\fR \fIlocal-port\fR
Forward a local TCP port to a remote peer's service. Runs in the foreground
until interrupted with Ctrl-C.
PLUGIN_MAN_PLACEHOLDER
.SH IDENTITY & ACCESS
Access control in shurli is based on
.I authorized_keys
files, similar to SSH. Each peer is identified by a libp2p peer ID (derived
from an Ed25519 keypair generated at init time). Only peers listed in your
authorized_keys file can connect.
.PP
Peers have roles:
.B admin
(can invite others, manage the relay) or
.B member
(can connect, use services). The first peer paired is automatically promoted
to admin.
.TP
.B whoami
Print your peer ID. This is the value other peers add to their authorized_keys.
.TP
.B auth add \fIpeer-id\fR [\fB--comment\fR \fI"..."\fR] [\fB--role\fR \fIadmin|member\fR]
Add a peer to your authorized_keys. The comment is for your reference only.
Default role: member.
.TP
.B auth list
List all authorized peers with their roles, comments, and verification status.
.TP
.B auth remove \fIpeer-id\fR
Revoke a peer. Takes effect immediately; existing connections from that peer
are terminated.
.TP
.B auth validate \fR[\fIfile\fR]
Check the authorized_keys file for syntax errors, duplicate entries, and
invalid peer IDs.
.TP
.B auth grant \fIpeer\fR [\fB--duration\fR \fI1h\fR] [\fB--services\fR \fIfile-transfer,...\fR] [\fB--permanent\fR]
Grant relay data access to a peer using macaroon capability tokens.
Default duration: 1 hour. When a peer has a grant, relay transport is
allowed for their plugin streams. Requires a running daemon.
.TP
.B auth grants
List all active data access grants with remaining time.
.TP
.B auth revoke \fIpeer\fR
Revoke a data access grant. All connections to the peer are closed immediately.
.TP
.B auth extend \fIpeer\fR \fB--duration\fR \fI2h\fR
Extend an existing grant. The new expiry is calculated from now, not from the
original expiry.

.SH CONFIGURATION
.TP
.B init \fR[\fB--dir\fR \fIpath\fR] [\fB--network\fR \fInamespace\fR]
Interactive first-time setup. Creates the config directory, generates an
Ed25519 identity key, prompts for a relay server address, writes config.yaml,
and installs shell completions and the man page. The \fB--network\fR flag
creates a private DHT namespace (peers in different namespaces cannot
discover each other).
.TP
.B config validate \fR[\fB--config\fR \fIpath\fR]
Parse and validate the config file. Reports errors without starting anything.
.TP
.B config show \fR[\fB--config\fR \fIpath\fR]
Print the fully resolved configuration (with defaults filled in and relative
paths expanded).
.TP
.B config set \fIkey\fR \fIvalue\fR [\fB--config\fR \fIpath\fR] [\fB--duration\fR \fI10m\fR]
Set a single config value using a dotted key path (e.g.,
\fBnetwork.force_private_reachability true\fR). Preserves YAML structure and
comments. Use \fB--duration\fR with \fBtransfer.receive_mode timed\fR to set
both the mode and duration in a single command. Apply without restart:
\fBshurli config reload\fR.
.TP
.B config rollback \fR[\fB--config\fR \fIpath\fR]
Replace the current config with the last-known-good backup (created
automatically before each \fBconfig apply\fR).
.TP
.B config apply \fInew-config\fR [\fB--confirm-timeout\fR \fIduration\fR]
Swap in a new config with a dead-man's switch: if \fBconfig confirm\fR is not
run within the timeout (default: 5 minutes), the previous config is restored
automatically. Designed for safe remote config changes.
.TP
.B config confirm \fR[\fB--config\fR \fIpath\fR]
Accept the currently applied config, cancelling the auto-revert timer.

.SH RELAY CLIENT COMMANDS
These commands manage relay server addresses in your local
.I shurli.yaml.
A node typically has one relay, but you can configure multiple for redundancy.
.TP
.B relay add \fIaddress\fR [\fB--peer-id\fR \fIID\fR]
Add a relay. Accepts a full multiaddr
(\fB/ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...\fR) or shorthand
(\fB203.0.113.50:7777\fR with \fB--peer-id\fR).
.TP
.B relay list
Show all configured relay addresses.
.TP
.B relay remove \fImultiaddr\fR [\fB--force\fR]
Remove a relay address. Refuses to remove the last one unless \fB--force\fR
is given, since the daemon needs at least one relay to start.

.SH RELAY SERVER COMMANDS
Run these on a VPS to operate a relay. Relay servers do not store any user
data and cannot decrypt traffic. They forward encrypted streams between
authorized peers.
.TP
.B relay setup \fR[\fB--dir\fR \fIpath\fR] [\fB--fresh\fR]
Generate a relay-server.yaml with sensible defaults. Backs up any existing
config first.
.TP
.B relay serve \fR[\fB--config\fR \fIpath\fR]
Start the relay. Listens on all configured addresses, accepts connections
from authorized peers, and relays traffic. Exposes Prometheus metrics on
the configured metrics port.
.TP
.B relay authorize \fIpeer-id\fR [\fIcomment\fR] [\fB--remote\fR \fIaddr\fR]
Add a peer to the relay's authorized_keys. Only authorized peers can use
the relay. Supports --remote for administration from any admin device.
.TP
.B relay deauthorize \fIpeer-id\fR [\fB--remote\fR \fIaddr\fR]
Remove a peer's relay access. Active connections from that peer are dropped.
Supports --remote for administration from any admin device.
.TP
.B relay set-attr \fIpeer-id\fR \fIkey\fR \fIvalue\fR [\fB--remote\fR \fIaddr\fR]
Set an attribute on a peer in the relay's authorized_keys. Allowed keys:
role (admin/member), group, verified.
Supports --remote for administration from any admin device.
.TP
.B relay grant \fIpeer-id\fR [\fB--duration\fR \fI1h\fR] [\fB--services\fR \fIsvc,...\fR] [\fB--permanent\fR] [\fB--remote\fR \fIaddr\fR]
Grant time-limited data relay access to a peer. Default duration is 1 hour.
Without a grant, peers can only use signaling protocols (pairing, discovery).
Admin peers always have data access regardless of grants.
.TP
.B relay grants [\fB--remote\fR \fIaddr\fR]
List all active data relay grants with remaining time.
.TP
.B relay revoke \fIpeer-id\fR [\fB--remote\fR \fIaddr\fR]
Revoke a peer's data relay grant and terminate all active circuits.
.TP
.B relay extend \fIpeer-id\fR \fB--duration\fR \fI2h\fR [\fB--remote\fR \fIaddr\fR]
Extend an existing data relay grant. The new expiry is calculated from now.
.TP
.B relay list-peers [\fB--remote\fR \fIaddr\fR]
Print all peers authorized to use this relay, with their roles and comments.
Supports --remote for administration from any admin device.
.TP
.B relay verify \fIpeer-id\fR
Verify a peer's identity using a Short Authentication String (SAS). Displays
emoji and numeric codes that must match on both sides. Marks the peer as
verified in the relay's authorized_keys on confirmation.
.TP
.B relay info
Display the relay's peer ID, all multiaddrs it is listening on, and a
QR code for easy mobile pairing.
.TP
.B relay invite create \fR[\fB--ttl\fR \fI1h\fR] [\fB--expires\fR \fIduration\fR] [\fB--remote\fR \fIaddr\fR]
Generate a single-use invite code. Share the code with the joining peer
who uses \fBshurli join <code>\fR. Codes expire after the TTL.
.TP
.B relay invite list \fR[\fB--remote\fR \fIaddr\fR]
List active invites with usage status.
.TP
.B relay invite revoke \fR\fIid\fR [\fB--remote\fR \fIaddr\fR]
Revoke an unused invite.
.TP
.B relay show
Show the resolved relay config (alias for relay config show).
.TP
.B relay config show
Show the resolved relay config with validation warnings and archive status.
.TP
.B relay config validate
Validate the relay config without starting.
.TP
.B relay config rollback
Restore the last-known-good relay config.
.TP
.B relay recover
Recover relay identity from a BIP39 seed phrase.
.TP
.B relay version
Show the relay binary version.

.SH RELAY VAULT
The vault encrypts the relay's private identity key at rest using
.B Argon2id
key derivation and
.B XChaCha20-Poly1305
authenticated encryption. When the vault is sealed, the relay enters
watch-only mode: it accepts connections and serves status, but cannot sign
or authenticate. This limits damage if the relay server is compromised.
.PP
The vault can be unsealed locally (entering the passphrase on the server)
or remotely over P2P from an admin peer using the
.B /shurli/relay-unseal/1.0.0
protocol. Optional TOTP 2FA can be required for unseal operations.
.TP
.B relay vault init \fR[\fB--totp\fR] [\fB--auto-seal\fR \fIminutes\fR]
Create a new vault. Prompts for a passphrase. \fB--totp\fR enables TOTP-based
two-factor authentication. \fB--auto-seal\fR sets an inactivity timer after
which the vault re-seals itself (default: 30 minutes, 0 for manual only).
.TP
.B relay vault seal
Seal the vault immediately. The relay enters watch-only mode.
.TP
.B relay vault unseal \fR[\fB--remote\fR \fImultiaddr\fR] [\fB--totp\fR]
Unseal the vault. Without \fB--remote\fR, prompts for the passphrase locally.
With \fB--remote\fR, connects to the relay over P2P and performs the unseal
operation remotely (requires admin role).
.TP
.B relay vault status
Show whether the vault is sealed or unsealed, time since last unseal,
and auto-seal timeout.
.TP
.B relay vault change-password
Change the vault password. Requires current password for authentication.
.TP
.B relay seal\fR, \fBrelay unseal\fR, \fBrelay seal-status
Shorthands for the vault subcommands above.

.SH RELAY INVITES
Invites use
.B macaroon
capability tokens (HMAC-SHA256 chains) for asynchronous peer onboarding.
An admin creates an invite deposit on the relay; a new peer picks it up later.
Macaroons support offline attenuation: you can add restrictions (caveats) to
an invite, but never widen access.
.PP
Seven caveat types are supported: service, action, peer, time, network,
role, and IP range.
.TP
.B relay invite create \fR[\fB--caveat\fR \fI"..."\fR] [\fB--ttl\fR \fIseconds\fR]
Create an invite deposit. Caveats are semicolon-separated:
.nf
  shurli relay invite create --caveat "service=ssh;role=member"
.fi
.TP
.B relay invite list
List all pending invite deposits.
.TP
.B relay invite revoke \fIid\fR
Delete an unclaimed invite.

.SS Operator Announcements (MOTD / Goodbye)
.TP
.B relay motd set \fImessage\fR [\fB--remote\fR \fIaddr\fR]
Set a message of the day. Shown to peers when they connect to the relay.
Messages are signed by the relay identity key and verified by clients.
Maximum 280 characters.
.TP
.B relay motd clear [\fB--remote\fR \fIaddr\fR]
Clear the MOTD. New connections will not see a message.
.TP
.B relay motd status [\fB--remote\fR \fIaddr\fR]
Show the current MOTD and goodbye status.
.TP
.B relay goodbye set \fImessage\fR [\fB--remote\fR \fIaddr\fR]
Set a goodbye announcement. Pushed to all connected peers immediately and
persisted to disk. Clients cache the goodbye and show it on reconnect attempts.
.TP
.B relay goodbye retract [\fB--remote\fR \fIaddr\fR]
Retract a goodbye announcement. Peers clear their cached goodbye.
.TP
.B relay goodbye shutdown \fR[\fImessage\fR] [\fB--remote\fR \fIaddr\fR]
Send a goodbye to all peers and shut down the relay. Use for planned
decommission with a clean handoff.

.SH ZKP ANONYMOUS AUTHENTICATION
Shurli supports zero-knowledge proof authentication using
.B gnark PLONK
on the BN254 curve with a
.B Poseidon2
Merkle tree. This allows a peer to prove they are a member of the authorized
set without revealing
.I which
member they are. The relay learns "this person is authorized" but not "this
is Alice."
.PP
The protocol works as a challenge-response:
.IP 1. 4
The relay sends a random nonce and the current Merkle root.
.IP 2. 4
The client generates a PLONK proof (~520 bytes, ~1.8 seconds).
.IP 3. 4
The relay verifies the proof (~2-3 milliseconds).
.PP
Range proofs are also supported for proving reputation scores fall within
a threshold without revealing the exact score.
.TP
.B relay zkp-setup \fR[\fB--keys-dir\fR \fIpath\fR] [\fB--force\fR]
Generate the PLONK circuit keys (proving key and verifying key). These are
deterministically derived from a BIP39 seed phrase, so the same seed always
produces the same keys. Prompts for the seed phrase interactively with hidden
input (no echo). The seed phrase is never exposed in process listings.
.TP
.B relay zkp-test \fB--auth-keys\fR \fIpath\fR [\fB--keys-dir\fR \fIpath\fR] [\fB--role\fR \fI0|1|2\fR]
Run a local end-to-end test: build Merkle tree, generate proof, verify proof.
No network connection required.

.SH SERVICES
Services are TCP endpoints on the local machine that you expose to authorized
peers. Each service has a name (e.g., "ssh", "grafana") and a local address
(e.g., "127.0.0.1:22"). Remote peers use
.B shurli proxy
to reach these services through the encrypted tunnel.
.TP
.B service add \fIname\fR \fIaddress\fR [\fB--protocol\fR \fIid\fR]
Register a new service. The address must be reachable on the local machine.
The optional \fB--protocol\fR overrides the default libp2p protocol ID.
.TP
.B service remove \fIname\fR
Remove a service. Active connections to it are dropped.
.TP
.B service enable \fIname\fR
Re-enable a previously disabled service.
.TP
.B service disable \fIname\fR
Stop accepting connections for this service without removing its config.
.TP
.B service list
List all services with their name, address, enabled status, and protocol ID.

.SH PAIRING
Pairing establishes mutual trust between two devices. It uses PAKE v1
(Password-Authenticated Key Exchange): X25519 Diffie-Hellman with
HKDF-SHA256 key derivation and XChaCha20-Poly1305 encryption. The shared
secret is the invite code itself, so no pre-existing secure channel is needed.
.PP
After pairing, both peers are added to each other's authorized_keys
automatically.
.TP
.B invite \fR[\fB--as\fR \fI"home"\fR] [\fB--ttl\fR \fIduration\fR] [\fB--non-interactive\fR]
Generate a one-time invite code and wait for a peer to join. The
\fB--as\fR flag sets your node's name on the network.
Default TTL: 10 minutes.
.TP
.B join \fIcode\fR [\fB--as\fR \fI"laptop"\fR] [\fB--non-interactive\fR]
Connect to the inviting peer using the code. Mutually authenticates, then
exchanges peer IDs and authorized_keys entries.
.TP
.B verify \fIpeer\fR
Perform SAS (Short Authentication String) verification. Both devices display
a 4-emoji fingerprint derived from their shared key material. If the emojis
match (verified out-of-band), the peer is marked as verified. Unverified
peers show an [UNVERIFIED] badge on all commands.

.SH IDENTITY SECURITY
.TP
.B recover \fR[\fB--relay\fR] [\fB--dir\fR \fIpath\fR]
Recover identity from a BIP39 seed phrase. Prompts for the seed phrase
interactively with hidden input (no echo), then prompts for a new password.
\fB--relay\fR also recovers the relay vault and ZKP keys.
.TP
.B change-password \fR[\fB--dir\fR \fIpath\fR]
Change the identity password. Prompts for the current password, then the new
password with confirmation. Updates the session token if one exists.
.TP
.B lock
Lock the daemon, disabling sensitive operations (service exposure, identity
actions) until unlocked. No flags.
.TP
.B unlock
Unlock a locked daemon with password verification. Restores full operation.
.TP
.B session refresh
Rotate the session token. Uses the same password but generates fresh
cryptographic material.
.TP
.B session destroy
Delete the session token. The daemon will require password entry on next start.

.SH PLUGINS
Manage the plugin system. Plugins extend Shurli with new capabilities
(file transfer, Wake-on-LAN, etc.) and can be enabled/disabled at runtime.
.TP
.B plugin list \fR[\fB--json\fR]
List all registered plugins with their name, version, type, and state.
.TP
.B plugin enable \fIname\fR
Enable a plugin. Registers its protocols and starts background work.
.TP
.B plugin disable \fIname\fR
Disable a plugin. Drains active connections (30s timeout) and unregisters protocols.
.TP
.B plugin info \fIname\fR \fR[\fB--json\fR]
Show detailed information about a plugin: commands, routes, protocols, config key, crash count.
.TP
.B plugin disable-all
Emergency kill switch. Disables all active plugins immediately.

.SH OTHER COMMANDS
.TP
.B status \fR[\fB--config\fR \fIpath\fR]
Show a summary of local configuration: config file path, identity, relay
addresses, authorized peers, and registered services.
.TP
.B doctor \fR[\fB--fix\fR]
Health check for your shurli installation. Verifies:
.RS
.IP \(bu 2
Config file exists and is valid
.IP \(bu 2
Identity key exists
.IP \(bu 2
Shell completions are installed and up to date
.IP \(bu 2
Man page is installed
.RE
.IP
Use \fB--fix\fR to automatically repair any issues found. After upgrading
shurli, run \fBdoctor --fix\fR to update completions and the man page for
new commands.
.TP
.B completion \fIbash\fR|\fIzsh\fR|\fIfish\fR
Print a shell completion script to stdout. Completions are installed
automatically by \fBshurli init\fR and updated by \fBshurli doctor --fix\fR.
.TP
.B man
Display this manual page.
.TP
.B version
Print version string, git commit hash, build date, and Go runtime.

.SH CONCEPTS

.SS Peer ID
Every shurli node has a unique peer ID, derived from its Ed25519 identity key.
The peer ID is a Base58-encoded multihash of the public key, prefixed with
"12D3KooW". It serves the same role as an SSH host key fingerprint: it
cryptographically identifies a machine regardless of its IP address.

.SS Connection Gating
When a remote peer attempts to connect, shurli's connection gater checks
the peer ID against the local authorized_keys file. Unauthorized peers are
rejected at the transport layer before any application data is exchanged.

.SS Private DHT
By default, shurli uses the DHT namespace \fB/shurli/kad/1.0.0\fR for peer
discovery. The \fB--network\fR flag during init creates a private namespace
(\fB/shurli/<name>/kad/1.0.0\fR), isolating your peer group from others.

.SS Config Safety
The \fBconfig apply\fR/\fBconfig confirm\fR pattern is inspired by
.BR junos-commit-confirm .
It prevents lockout on remote machines: if a config change breaks
connectivity, the automatic revert restores access without manual
intervention.

.SH SHELL COMPLETION
Tab completion is installed automatically by \fBshurli init\fR. If completions
are missing or outdated, run:
.nf
  shurli doctor --fix
.fi

.SH FILES
.TP
.I ~/.config/shurli/config.yaml
Default node configuration file. Contains relay addresses, service
definitions, peer names, and DHT settings.
.TP
.I ~/.config/shurli/identity.key
Ed25519 private key. Generated once during \fBshurli init\fR. Back this
file up; losing it means generating a new identity and re-pairing with
all peers.
.TP
.I ~/.config/shurli/authorized_keys
Peer allowlist. One entry per line. Supports attributes: role (admin/member),
comment, expires, verified, group. Syntax is validated by \fBauth validate\fR.
.TP
.I ./shurli.yaml
Alternate config location. Shurli searches the current directory first,
then ~/.config/shurli/config.yaml. Override with \fB--config\fR.
.TP
.I relay-server.yaml
Relay server configuration. Contains listen addresses, authorized_keys path,
metrics settings, and vault configuration.
.TP
.I /usr/local/share/man/man1/shurli.1
Installed man page (created by \fBshurli init\fR or \fBshurli man --install\fR).

.SH ENVIRONMENT
.TP
.B SHELL
Used by \fBshurli init\fR and \fBshurli doctor\fR to detect your shell and
install the correct completion script.
.TP
.B SHURLI_INVITE_CODE
Invite code for the \fBjoin\fR command. Alternative to the positional argument.
Useful for scripted or non-interactive pairing.

.SH SECURITY CONSIDERATIONS
.IP \(bu 2
All peer-to-peer connections use libp2p's Noise protocol for transport
encryption (XX handshake pattern). The relay server sees only encrypted
bytes.
.IP \(bu 2
The relay cannot impersonate peers. It forwards traffic but does not hold
any peer's private key.
.IP \(bu 2
Invite codes are single-use, time-limited, and derived from cryptographically
random material. A stolen code can be used only once.
.IP \(bu 2
The vault uses Argon2id (memory-hard KDF) to derive the encryption key from
your passphrase. Even if the relay's disk is compromised, the identity key
is protected.
.IP \(bu 2
ZKP authentication reveals nothing beyond "this peer is authorized." The
relay learns membership but not identity.
.IP \(bu 2
The \fBverify\fR command guards against MITM attacks during pairing. Without
verification, a compromised relay could theoretically substitute its own
identity during the PAKE exchange. Verification makes this detectable.

.SH DIAGNOSTICS
Most commands exit with status 0 on success and 1 on error. Error messages
are printed to stderr.
.PP
The daemon logs to stderr using Go's structured logging (\fBslog\fR).
Default level is INFO. Set the log level in your config or use environment
variables for debugging.
.PP
Use \fBshurli doctor\fR to diagnose installation issues.

.SH SEE ALSO
.UR https://shurli.io
Shurli documentation
.UE
.PP
.UR https://docs.libp2p.io
libp2p documentation
.UE

.SH AUTHORS
Shurli is developed by Satinder Grewal.

.SH HISTORY
Shurli began as a personal tool to securely connect devices across multiple
ISPs and network types without trusting any third party. Inspired by operators
who chose to shut down rather than compromise their users.
`
	// Inject plugin man page sections (only enabled plugins).
	allCmds := plugin.CLICommandDescriptions()
	var enabledCmds []plugin.CLICommandEntry
	for _, cmd := range allCmds {
		if isPluginEnabledInConfig(cmd.PluginName) {
			enabledCmds = append(enabledCmds, cmd)
		}
	}
	if len(enabledCmds) > 0 {
		pluginSection := plugin.GenerateManSection(enabledCmds)
		page = strings.Replace(page, "PLUGIN_MAN_PLACEHOLDER", pluginSection, 1)
	} else {
		page = strings.Replace(page, "PLUGIN_MAN_PLACEHOLDER\n", "", 1)
	}
	return page
}
