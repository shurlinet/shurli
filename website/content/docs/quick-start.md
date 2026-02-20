---
title: "Quick Start"
weight: 1
---
<!-- Auto-synced from README.md by sync-docs.sh — do not edit directly -->


### Path A: Joining someone's network

If someone shared an invite code with you:

```bash
# Install (or build from source: go build -o peerup ./cmd/peerup)
peerup join <invite-code> --name laptop
```

That's it. You're connected and mutually authorized.

### Path B: Setting up your own network

**1. Set up both machines:**
```bash
go build -o peerup ./cmd/peerup
peerup init
```

**2. Pair them (on the first machine):**
```bash
peerup invite --name home
# Shows invite code + QR code, waits for the other side...
```

**3. Join (on the second machine):**
```bash
peerup join <invite-code> --name laptop
```

**4. Use it:**
```bash
# On the server — start the daemon with services exposed
peerup daemon

# On the client — connect to a service
peerup proxy home ssh 2222
ssh -p 2222 user@localhost
```

> **Relay server**: Both machines connect through a relay for NAT traversal. See [relay-server/README.md](https://github.com/satindergrewal/peer-up/blob/main/relay-server/README.md) for deploying your own. Run `peerup relay serve` to start a relay. A shared relay is used by default during development.
