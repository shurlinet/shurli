---
title: "Design Philosophy"
weight: 1
description: "Why Shurli uses no accounts, no central servers, and no vendor dependencies."
---
<!-- Auto-synced from docs/faq/design-philosophy.md by sync-docs - do not edit directly -->


## Why no accounts or central servers?

Most remote access tools require you to create an account on someone else's server. That server becomes a single point of failure and a single point of control. If the company changes pricing, shuts down, gets acquired, or suffers a breach, your access breaks and your connection metadata is exposed.

Shurli eliminates that dependency entirely. Identity is an Ed25519 key pair generated locally on your machine. Authorization is a plain-text file listing which peer IDs are allowed to connect - the same model as SSH's `authorized_keys`. Configuration is one YAML file you can read, edit, back up, and version-control yourself.

There is no registration step, no authentication token issued by a third party, and no renewal that can lapse. Two machines can find and authenticate each other using only their local keys and a shared relay for the initial connection. If you want full independence, run your own relay on any VPS - the entire system operates with zero external accounts.

The result: your ability to reach your own machines depends on your infrastructure, not someone else's business decisions.

---

## Why Shurli avoids centralized identity

When a remote access tool requires you to sign in with an identity provider, that provider becomes a silent dependency in your infrastructure. If the provider has an outage, you cannot authenticate. If they change their terms, your access is conditional on compliance. If they are breached, your device graph, connection history, and metadata are exposed alongside every other user on the platform.

Shurli sidesteps this entirely. Identity is an Ed25519 key pair generated on your machine during `shurli init`. It never leaves your device, is never uploaded, and is never registered with any external service. Authentication happens directly between peers: each side checks the other's public key against a local `authorized_keys` file. No OAuth flow, no email verification, no session tokens issued by a third party.

The practical result: your identity cannot be suspended, revoked, or invalidated by anyone other than you. If every server on the internet went offline except your two machines and a relay, Shurli would still authenticate and connect them.

---

## Why Shurli rejects vendor lock-in

Remote access platforms typically control three things: the relay infrastructure your traffic passes through, the identity system that proves who you are, and the transport protocol that carries your data. When a single vendor controls all three, switching costs compound. Your device configurations point to their relays, your identity exists only in their database, and your client software speaks a proprietary protocol that nothing else understands. If the vendor raises prices, changes terms, or shuts down, you rebuild from scratch.

Shurli decouples all three layers. Relays are standard libp2p circuit relay v2 nodes - run your own on any VPS, or use someone else's, or run none if your peers have direct reachability. Identity is a local Ed25519 key pair with no external registration. Transport is QUIC and TCP through the libp2p stack, an open protocol with multiple independent implementations across languages. Nothing in the system is proprietary, vendor-specific, or non-replaceable.

The design consequence: every component can be swapped, self-hosted, or eliminated without losing access to your network. There is no migration path to plan because there is nothing to migrate away from.

---

**Last Updated**: 2026-02-24
