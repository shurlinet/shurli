---
title: "Connections That Don't Give Up"
date: 2026-02-10
tags: [release, batch-a]
image: /images/blog/batch-a-reliability.svg
description: "peer-up connections now retry automatically with exponential backoff. Network drops recover without intervention."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Connections That Don't Give Up](/images/blog/batch-a-reliability.svg)

## What's new

peer-up connections now retry automatically with exponential backoff when they fail. If your relay hiccups or a transient network issue drops a connection, peer-up reconnects without you lifting a finger.

TCP proxies have proper timeout handling: dial timeouts for initial connections and half-close propagation for clean shutdown. SSH sessions through relay stay alive as long as you need them.

## Why it matters

P2P connections through circuit relay are inherently less reliable than direct connections. A proxy that dies on the first hiccup is useless for real work like SSH sessions or remote desktop. Automatic retry with backoff means you can start an SSH session through your relay and trust it to survive brief network interruptions.

## Technical highlights

![Exponential backoff with cap - retry intervals double each attempt up to 60 seconds](/images/blog/batch-a-backoff-timeline.svg)

- **Exponential backoff retry**: 1s → 2s → 4s → ... capped at 60s. Wraps any dial function via `DialWithRetry()`
- **TCP timeout strategy**: 10s dial timeout, 30s service connection timeout. Long-lived sessions (SSH) are not killed by idle timers
- **Half-close propagation**: When one side of a proxied connection finishes sending, `CloseWrite()` signals the other side cleanly, no data loss
- **DHT in proxy path**: Before connecting to a service, the proxy now performs DHT discovery to find the target peer. No need to manually reconnect first
- **In-process integration tests**: Real libp2p hosts communicating through an in-process relay. Fast (2s), runs anywhere

## What's next

Code quality improvements: structured logging, sentinel errors, and build version embedding.
