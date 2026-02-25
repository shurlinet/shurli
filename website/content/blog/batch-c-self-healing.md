---
title: "Your Network Fixes Itself"
date: 2026-02-13
tags: [release, batch-c]
image: /images/blog/batch-c-self-healing.svg
description: "Automatic config rollback, commit-confirmed pattern, and systemd watchdog integration for unattended recovery."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Your Network Fixes Itself](/images/blog/batch-c-self-healing.svg)

## What's new

Shurli can now roll back bad config changes automatically. Change your relay address and lose connectivity? The daemon reverts your config and restarts with the working version, no manual intervention needed, even if you can't SSH in anymore.

## Why it matters

The nightmare scenario for any remote-access tool: you change the config on your home server, the new config breaks connectivity, and now you can't reach the machine to fix it. Network engineers have solved this for decades with "commit confirmed," and now Shurli has it too.

## Technical highlights

![The commit-confirmed pattern - apply config, start timer, auto-rollback if not confirmed](/images/blog/batch-c-commit-confirmed.svg)

- **Config archive/rollback**: Before any config change, the current config is archived to `.config.last-good.yaml` with atomic writes (temp file + rename). `shurli config rollback` restores it instantly
- **Commit-confirmed pattern**: `shurli config apply new.yaml --confirm-timeout 5m` applies the change and starts a timer. If you don't run `shurli config confirm` within 5 minutes, the config auto-reverts and the daemon restarts with the known-good version
- **Watchdog + sd_notify**: Pure Go implementation of systemd's sd_notify protocol (READY=1, WATCHDOG=1, STOPPING=1). The watchdog runs health checks every 30 seconds and only reports healthy when all checks pass. No-op on macOS, same binary works everywhere
- **Extensible health checks**: The daemon adds a socket health check to verify its API is still accepting connections. Future batches will add libp2p connection health

## What's next

Leveraging more libp2p features: AutoNAT v2, QUIC-preferred transport ordering, and smarter dialing.
