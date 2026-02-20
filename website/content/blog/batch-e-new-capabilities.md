---
title: "Status, Health Checks, and Scriptable Pairing"
date: 2026-02-15
tags: [release, batch-e]
image: /images/blog/batch-e-capabilities.svg
---

![Status, Health Checks, and Scriptable Pairing](/images/blog/batch-e-capabilities.svg)

## What's new

You can now check your node's status with `peerup status`, monitor the relay server with a `/healthz` endpoint, and pair devices non-interactively — making Docker deployments and CI/CD pipelines first-class citizens.

## Why it matters

A P2P tool that can't tell you its own status is a black box. Operators need health endpoints for monitoring. And if pairing requires a human to scan a QR code, you can't automate deployments. These capabilities close the gap between "works on my laptop" and "production-ready."

## Technical highlights

![Headless pairing pipeline — from Docker environment variable to network member, zero human interaction](/images/blog/batch-e-headless-pipeline.svg)

- **`peerup status`**: Shows local config, services, relay addresses, and peer ID in a clean summary. No daemon required — reads the config file directly
- **`/healthz` endpoint**: HTTP health check on the relay server (default `127.0.0.1:9090`). Returns status, uptime, and connected peer count. Restricted to loopback for security
- **Headless invite/join**: `peerup invite --non-interactive` prints the bare invite code to stdout (progress to stderr). `peerup join <code> --non-interactive` accepts programmatically. Environment variable `PEERUP_INVITE_CODE` also supported
- **Docker-friendly pairing**: `PEERUP_INVITE_CODE=xxx peerup join --non-interactive --name node-1` — one line to join a network from a container

## What's next

Full daemon mode with a Unix socket API — turning peer-up into a long-running service with a rich control interface.
