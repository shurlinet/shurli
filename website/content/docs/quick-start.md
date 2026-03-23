---
title: "Quick Start"
weight: 2
description: "Get two devices connected with Shurli in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel."
---
<!-- Auto-synced from README.md by sync-docs - do not edit directly -->


## 0. Install Shurli

Build from source ([Go 1.26+](https://go.dev/dl/) required):
```bash
git clone https://github.com/shurlinet/shurli.git
cd shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli
sudo install -m 755 shurli /usr/local/bin/shurli
```

Linux also needs mDNS: `sudo apt install libavahi-compat-libdnssd-dev` (Debian/Ubuntu).

## 1. Deploy a relay (recommended)

Follow the [Relay Setup Guide](https://github.com/shurlinet/shurli/blob/main/docs/RELAY-SETUP.md) to deploy your own relay on any VPS.
This gives you full capability: data relay, file transfer, service proxy through NAT.

Without your own relay, Shurli uses public seed nodes for peer discovery only.
Direct connections work, but data relay through seeds is blocked by design.

## 2. Initialize (first time only)

```bash
shurli init
```

Interactive wizard: creates your identity, sets a password, and writes your configuration.
Choose "Use my own relay server" (option 1) and enter your relay address.

## 3. Connect

**I have an invite code:**

```bash
shurli join <invite-code> --as laptop
```

Done. You're connected and mutually authorized.

**I want to set up my own network:**

On machine 1 (your server):
```bash
shurli invite --as home
# Shows invite code + QR code, waits for the other side...
```

On machine 2 (your client):
```bash
shurli join <invite-code> --as laptop
```

## 4. Use it

```bash
# On the server - start the daemon
shurli daemon

# On the client - connect to a service
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

## Disclaimer

Shurli is experimental software under active development. It is provided "as is" with no warranty of any kind (see [LICENSE](LICENSE)). If you discover a bug, please [open an issue](https://github.com/shurlinet/shurli/issues).
