---
title: "Quick Start"
weight: 1
description: "Get two devices connected with Shurli in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel."
---
<!-- Auto-synced from README.md by sync-docs - do not edit directly -->


## Path A: Joining someone's network

If someone shared an invite code with you:

```bash
# Install (or build from source: go build -o shurli ./cmd/shurli)
shurli join <invite-code> --name laptop
```

That's it. You're connected and mutually authorized.

## Path B: Setting up your own network

**1. Set up both machines:**
```bash
go build -o shurli ./cmd/shurli
shurli init
```

**2. Pair them (on the first machine):**
```bash
shurli invite --name home
# Shows invite code + QR code, waits for the other side...
```

**3. Join (on the second machine):**
```bash
shurli join <invite-code> --name laptop
```

**4. Use it:**
```bash
# On the server - start the daemon with services exposed
shurli daemon

# On the client - connect to a service
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

## Path C: Relay-managed group setup

If a relay admin shared a pairing code:

```bash
shurli join <pairing-code> --name laptop
# Connects to relay, discovers other peers, auto-authorizes everyone
# Shows SAS verification fingerprints for each peer
```

The relay admin generates codes with `shurli relay pair --count 3` (for 3 peers). Each person joins with one command. Everyone in the group is mutually authorized and verified.

> **Relay server**: All machines connect through a relay for NAT traversal. See [Relay Setup guide](../relay-setup/) for deploying your own. Run `shurli relay serve` to start a relay.

## Disclaimer

Shurli is experimental software under active development. It is built with significant AI assistance (Claude) and, despite thorough testing, **will contain bugs** that neither automated tests nor manual testing have caught.

**By using this software, you acknowledge:**

- This is provided "as is" with no warranty of any kind (see [LICENSE](LICENSE))
- The developers are not liable for any damages, losses, or consequences arising from its use
- Network tunnels may disconnect, services may become unreachable, and configurations may behave unexpectedly
- This is not a replacement for enterprise VPN, firewall, or security infrastructure
- You are responsible for evaluating whether Shurli is suitable for your use case

If you discover a bug, please [open an issue](https://github.com/shurlinet/shurli/issues). Every report makes the project more reliable for everyone.

## Next steps

Now that you're connected, follow the setup journey:

1. [Relay Setup](../relay-setup/) - deploy your own relay server on a VPS
2. [Securing Your Relay](../relay-security/) - vault, 2FA, auto-seal, remote unseal
3. [Inviting Peers](../inviting-peers/) - pairing codes and async invite deposits
4. [Managing Your Network](../managing-network/) - roles, permissions, day-to-day operations
5. [Monitoring](../monitoring/) - Prometheus and Grafana dashboards
