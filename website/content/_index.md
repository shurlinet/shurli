---
title: Shurli
description: "Connect your devices directly - no accounts, no cloud, no subscriptions. Works through NAT, CGNAT, and firewalls."
layout: hextra-home
---

<div class="shurli-hero-logo">
  <img src="/images/logo.png" alt="Shurli logo" width="96" height="96" />
</div>

{{< hextra/hero-badge >}}
  <div class="hx:w-2 hx:h-2 hx:rounded-full hx:bg-primary-400"></div>
  <span>Open source &middot; Self-sovereign</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="shurli-hero-hook hx:mt-4 hx:mb-2">
  Can't reach your home server from outside? Neither could we.
</div>

<div class="hx:mt-2 hx:mb-6">
{{< hextra/hero-headline >}}
  Shurli just&nbsp;<br class="hx:sm:block hx:hidden" />connects.
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-8">
{{< hextra/hero-subtitle >}}
  Connect your devices directly - no accounts, no cloud, no subscriptions.&nbsp;<br class="hx:sm:block hx:hidden" />Works even when your network blocks everything.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-12 hx:flex hx:flex-wrap hx:gap-4 hx:justify-center">
{{< hextra/hero-button text="Get Started" link="docs/quick-start" >}}
<a href="https://github.com/shurlinet/shurli" target="_blank" rel="noopener" class="shurli-secondary-btn">
  {{< icon name="github" attributes="height=18" >}}
  <span>View on GitHub</span>
</a>
</div>

<!-- ============================================================ -->
<!-- SECTION: Terminal Demo                                        -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="lightning-bolt" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">From zero to connected in 60 seconds</h2>
  <p class="shurli-section-subtitle">Two commands on each device. No accounts to create, no keys to exchange manually, no ports to forward.</p>
  <div class="shurli-demo-container">
    <img src="/images/terminal-demo.svg" alt="Shurli terminal demo showing init, invite, join, and proxy commands" class="shurli-demo-image" loading="lazy" />
  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: How It Works                                         -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="cog" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">How it works</h2>
  <p class="shurli-section-subtitle">Three steps. Both devices end up in each other's authorized_keys. That's it.</p>

  <div class="shurli-steps-grid">
    <div class="shurli-step">
      <div class="shurli-step-number">1</div>
      <img src="/images/how-it-works-1-init.svg" alt="Step 1: Initialize Shurli on your server" class="shurli-step-image" loading="lazy" />
      <h3 class="shurli-step-title">Initialize</h3>
      <p class="shurli-step-desc">Run <code>shurli init</code> on your server. It creates a unique identity and comes online, ready to accept connections.</p>
    </div>
    <div class="shurli-step">
      <div class="shurli-step-number">2</div>
      <img src="/images/how-it-works-2-invite.svg" alt="Step 2: Create and share invite code" class="shurli-step-image" loading="lazy" />
      <h3 class="shurli-step-title">Invite</h3>
      <p class="shurli-step-desc">Run <code>shurli invite</code> to get a one-time code. Send it however you like - text, email, Signal, carrier pigeon.</p>
    </div>
    <div class="shurli-step">
      <div class="shurli-step-number">3</div>
      <img src="/images/how-it-works-3-connect.svg" alt="Step 3: Join and start proxying services" class="shurli-step-image" loading="lazy" />
      <h3 class="shurli-step-title">Connect</h3>
      <p class="shurli-step-desc">Run <code>shurli join</code> on your laptop. Both devices trust each other automatically. Access any service through the encrypted connection.</p>
    </div>
  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: Feature Grid (existing, unchanged)                   -->
<!-- ============================================================ -->

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Works Through Anything"
    icon="globe-alt"
    subtitle="5G, hotel WiFi, corporate networks, double NAT - if your device has internet, Shurli finds a way through. Tested on the networks that block everything."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(59,130,246,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="One File, Nothing Else"
    icon="cube"
    subtitle="Download one file. Run it. Done. No containers, no runtimes, no databases to set up. Works offline after the initial pairing."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(16,185,129,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="You Control Who Connects"
    icon="shield-check"
    subtitle="A simple file on your device decides who gets in. No accounts to manage, no tokens to rotate, no company in the middle. Your network, your rules."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(245,158,11,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Two Commands to Connect"
    icon="terminal"
    subtitle="One command on your server, one on your laptop. Share a code, done. From zero to connected in about 60 seconds."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(139,92,246,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Access Any Service"
    icon="server"
    subtitle="Remote desktop, file servers, databases, web apps - anything running on your home network, accessible from anywhere as if you were on the same WiFi."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(236,72,153,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Stays Connected"
    icon="refresh"
    subtitle="Network drops? It reconnects automatically. Bad config? It rolls back. Shurli monitors itself and recovers without you lifting a finger."
    style="background: radial-gradient(ellipse at 50% 80%,rgba(20,184,166,0.15),hsla(0,0%,100%,0));"
  >}}
{{< /hextra/feature-grid >}}

<!-- ============================================================ -->
<!-- SECTION: Network Diagram                                      -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="switch-horizontal" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">Direct when possible, relayed when necessary</h2>
  <p class="shurli-section-subtitle">Shurli tries to connect your devices directly. When the network won't allow it, traffic flows through an encrypted relay, which never sees your data.</p>
  <div class="shurli-diagram-container">
    <img src="/images/network-diagram.svg" alt="Network diagram showing peer-to-peer connections through NAT with relay fallback" class="shurli-diagram-image" loading="lazy" />
  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: Install                                              -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="download" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">Install</h2>
  <p class="shurli-section-subtitle">Single binary. No runtime dependencies. Build from source with Go.</p>

  <div class="shurli-install-container">

{{< tabs items="macOS,Linux,From Source" >}}
{{< tab >}}
```bash
# Clone and build
git clone https://github.com/shurlinet/shurli.git
cd Shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli

# Move to PATH
sudo mv shurli /usr/local/bin/

# Verify
shurli version
```
{{< /tab >}}
{{< tab >}}
```bash
# Clone and build
git clone https://github.com/shurlinet/shurli.git
cd Shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli

# Move to PATH
sudo mv shurli /usr/local/bin/

# Verify
shurli version
```
{{< /tab >}}
{{< tab >}}
```bash
# Requires Go 1.22+
git clone https://github.com/shurlinet/shurli.git
cd Shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli

# Run directly
./shurli version
```
{{< /tab >}}
{{< /tabs >}}

  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: Bottom CTA                                           -->
<!-- ============================================================ -->

<div class="shurli-section shurli-cta-section">
  <div class="shurli-cta-grid">
    <a href="https://github.com/shurlinet/shurli" target="_blank" rel="noopener" class="shurli-cta-card">
      <div class="shurli-cta-icon">{{< icon name="github" attributes="height=28" >}}</div>
      <h3 class="shurli-cta-title">Star on GitHub</h3>
      <p class="shurli-cta-desc">Browse the source, open issues, contribute</p>
    </a>
    <a href="docs/quick-start" class="shurli-cta-card">
      <div class="shurli-cta-icon">{{< icon name="book-open" attributes="height=28" >}}</div>
      <h3 class="shurli-cta-title">Documentation</h3>
      <p class="shurli-cta-desc">Quick start, architecture, daemon API</p>
    </a>
    <a href="blog" class="shurli-cta-card">
      <div class="shurli-cta-icon">{{< icon name="pencil" attributes="height=28" >}}</div>
      <h3 class="shurli-cta-title">Blog</h3>
      <p class="shurli-cta-desc">Engineering updates and release notes</p>
    </a>
  </div>
</div>
