---
title: "The Daemon: A Full Control Plane"
date: 2026-02-16
tags: [release, batch-f]
image: /images/blog/batch-f-daemon.svg
---

![The Daemon: A Full Control Plane](/images/blog/batch-f-daemon.svg)

## What's new

`peerup daemon` is now a long-running service with a full REST API over Unix socket. Create proxies on the fly, ping peers, list services, manage authorized peers — all through the daemon while it maintains your P2P connections in the background.

## Why it matters

Before daemon mode, every operation started a fresh P2P host, connected to the relay, bootstrapped the DHT, and then did its thing. That's 5-10 seconds of startup for a simple ping. With the daemon running, operations are instant — the P2P host is already connected and ready.

More importantly, the daemon can manage multiple proxies simultaneously. Start an SSH proxy to your home server, an XRDP proxy to your desktop, and an HTTP proxy to your NAS — all through a single daemon instance.

## Technical highlights

![Before vs after daemon mode — from 5-10 second startup per command to instant responses](/images/blog/batch-f-before-after.svg)

- **Unix domain socket**: `~/.config/peerup/peerup.sock` with `umask(0077)` for atomic permissions. No TCP port conflicts, filesystem-level access control
- **Cookie authentication**: 32-byte random hex token written to `.daemon-cookie` (mode `0600`). Rotated every daemon restart. Sent as `Authorization: Bearer <token>`
- **14 REST endpoints**: status, services, peers, auth (add/remove/list), ping, traceroute, resolve, connect/disconnect proxies, expose/unexpose services, shutdown
- **Hot-reload authorized_keys**: Add or remove peers via `peerup daemon auth add <peer-id>` — takes effect immediately, no restart needed
- **RuntimeInfo interface**: Clean decoupling between daemon package and cmd package. Easy to mock in tests
- **Text and JSON output**: Every endpoint supports `Accept: text/plain` for human-readable output and `application/json` for scripting

## What's next

Comprehensive test coverage and documentation — including Docker integration tests and this engineering journal.
