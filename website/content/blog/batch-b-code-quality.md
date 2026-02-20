---
title: "Clean Code, Clear Errors"
date: 2026-02-12
tags: [release, batch-b]
image: /images/blog/batch-b-code-quality.svg
---

![Clean Code, Clear Errors](/images/blog/batch-b-code-quality.svg)

## What's new

Every error in peer-up is now a named, checkable sentinel value. Structured logging via `log/slog` replaces ad-hoc `log.Printf` calls everywhere. And every binary now self-identifies with its exact build version.

## Why it matters

When you're debugging a P2P connection issue at 2am, you need to know exactly which version is running on each peer and what went wrong. "Service not found" as a string is useless for programmatic error handling. `errors.Is(err, ErrServiceNotFound)` is actionable.

## Technical highlights

![From printf to structured logging - before and after comparison](/images/blog/batch-b-structured-logging.svg)

- **Sentinel errors**: `ErrServiceNotFound`, `ErrNameNotFound`, `ErrConfigNotFound`, `ErrNoArchive`, and more. All checkable with `errors.Is()`
- **Structured logging**: `slog.Info("connection succeeded", "attempt", 3, "peer", peerID[:16]+"...")` - key-value pairs, not format strings
- **Build version embedding**: `-ldflags "-X main.version=... -X main.commit=..."` at build time. `peerup version` shows exact build info
- **UserAgent in Identify**: Every peer announces its version (`peerup/0.1.0`). `peerup daemon peers` shows what version each peer runs
- **Relay address deduplication**: Same relay with IPv4 and IPv6 addresses? Merged into one entry automatically

## What's next

Self-healing capabilities: config archive/rollback and the commit-confirmed pattern.
