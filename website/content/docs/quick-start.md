---
title: "Quick Start"
weight: 1
description: "Get two devices connected with Shurli in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel."
---
<!-- Auto-synced from README.md by sync-docs - do not edit directly -->


## 1. Initialize (first time only)

```bash
shurli init
```

Interactive wizard: creates your identity (seed phrase backup), sets a password, and writes your configuration.

## 2. Connect

**I have an invite code:**

```bash
shurli join <invite-code> --name laptop
```

Done. You're connected and mutually authorized.

**I want to set up my own network:**

On machine 1 (your server):
```bash
shurli invite --name home
# Shows invite code + QR code, waits for the other side...
```

On machine 2 (your client):
```bash
shurli join <invite-code> --name laptop
```

## 3. Use it

```bash
# On the server - start the daemon
shurli daemon

# On the client - connect to a service
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

## Disclaimer

Shurli is experimental software under active development. It is provided "as is" with no warranty of any kind (see [LICENSE](LICENSE)). If you discover a bug, please [open an issue](https://github.com/shurlinet/shurli/issues).
