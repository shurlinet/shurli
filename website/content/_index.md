---
title: Shurli
description: "AI-native P2P networking for the Zero-Human Network. Connect devices and agents directly - no accounts, no cloud, no central authority. Works through NAT, CGNAT, and firewalls."
layout: hextra-home
---

<div class="shurli-hero-logo">
  <img src="/images/symbol-centered.svg" alt="Shurli logo" width="96" height="96" />
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
  Connect your devices and agents directly - no accounts, no cloud, no subscriptions.&nbsp;<br class="hx:sm:block hx:hidden" />Works even when your network blocks everything.
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
  <p class="shurli-section-subtitle">Deploy a relay, then two commands on each device. No accounts to create, no keys to exchange manually, no ports to forward.</p>
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
  <p class="shurli-section-subtitle">Deploy a relay, initialize your devices, and pair them. Your network, your rules.</p>

  <div class="shurli-steps-grid">
    <div class="shurli-step">
      <div class="shurli-step-number">1</div>
      <img src="/images/how-it-works-1-init.svg" alt="Step 1: Deploy relay and initialize Shurli" class="shurli-step-image" loading="lazy" />
      <h3 class="shurli-step-title">Setup</h3>
      <p class="shurli-step-desc">Deploy a relay on any VPS with <code>shurli relay setup</code>, then run <code>shurli init</code> on your devices. Your relay, your rules.</p>
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
<!-- SECTION: Where This Is Going                                  -->
<!-- ============================================================ -->

<div class="shurli-section shurli-vision-section">
  <div class="shurli-section-icon">{{< icon name="chip" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">The <a href="/blog/development-philosophy/#the-zero-human-network">Zero-Human Network</a></h2>
  <div class="shurli-vision-statement">
    <p>Zero-human companies are coming. They need a network that operates itself - where agents connect, negotiate, and transact directly. No cloud middleman. No central authority. Intelligence at every node.</p>
    <p><strong>Shurli is that network.</strong></p>
    <p>What connects your devices today is the foundation for what agents will run on tomorrow.</p>
  </div>
</div>

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
<!-- SECTION: Comparison Table                                     -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="scale" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">How Shurli compares</h2>
  <p class="shurli-section-subtitle">Different architectures make different trade-offs. Here's where Shurli sits.</p>

  <div class="shurli-compare-wrap">
    <table class="shurli-compare-table">
      <thead>
        <tr>
          <th></th>
          <th class="shurli-col">Shurli</th>
          <th>Centralized VPN Services</th>
          <th>Tunnel / Proxy Services</th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td>Account required</td>
          <td>None</td>
          <td>Yes (vendor)</td>
          <td>Yes (vendor)</td>
        </tr>
        <tr>
          <td>Control plane</td>
          <td>None (DHT)</td>
          <td>Vendor server</td>
          <td>Vendor server</td>
        </tr>
        <tr>
          <td>Identity</td>
          <td>Your Ed25519 keys</td>
          <td>Vendor-managed</td>
          <td>Vendor-managed</td>
        </tr>
        <tr>
          <td>NAT traversal</td>
          <td>Relay + hole-punching</td>
          <td>Vendor relays + STUN</td>
          <td>Vendor tunnels</td>
        </tr>
        <tr>
          <td>Source code</td>
          <td>Fully open</td>
          <td>Client open, server proprietary</td>
          <td>Varies</td>
        </tr>
        <tr>
          <td>Self-hostable</td>
          <td>Everything</td>
          <td>Partial</td>
          <td>No</td>
        </tr>
        <tr>
          <td>Data visibility</td>
          <td>Zero (relay sees nothing)</td>
          <td>Coordination server sees device graph</td>
          <td>Tunnel endpoint sees traffic</td>
        </tr>
      </tbody>
    </table>
  </div>
  <div style="text-align: center;">
    <a href="docs/faq/comparisons/" class="shurli-compare-link">Detailed comparisons &rarr;</a>
  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: Who Is This For                                      -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="users" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">Who is Shurli for?</h2>
  <p class="shurli-section-subtitle">Shurli is opinionated. It's built for a specific kind of user.</p>

  <div class="shurli-persona-grid">
    <div class="shurli-persona-card shurli-persona-for">
      <div class="shurli-persona-heading">Built for</div>
      <ul class="shurli-persona-list">
        <li>Self-hosters who can't reach their servers from outside</li>
        <li>Privacy-conscious users who refuse vendor accounts</li>
        <li>Home lab operators behind CGNAT or double NAT</li>
        <li>Developers exposing local services (Ollama, databases, web apps)</li>
        <li>AI agents that need direct peer-to-peer connectivity</li>
        <li>Anyone who wants their network under their own control</li>
      </ul>
    </div>
    <div class="shurli-persona-card shurli-persona-not">
      <div class="shurli-persona-heading">Not designed for</div>
      <ul class="shurli-persona-list">
        <li>Teams needing a web dashboard and SSO</li>
        <li>Users who want mobile apps (not yet available)</li>
        <li>Organizations requiring full IP-layer VPN</li>
        <li>People looking for zero-config "install and forget"</li>
      </ul>
    </div>
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
<!-- SECTION: What's Working Today                                 -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="check-circle" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">What's working today</h2>
  <p class="shurli-section-subtitle">Shurli is experimental, but the core is solid and tested on real networks.</p>

  <div class="shurli-status-grid">
    <div class="shurli-status-column shurli-status-shipped">
      <div class="shurli-status-heading shurli-status-shipped">Shipped</div>
      <ul class="shurli-status-list">
        <li>Encrypted peer-to-peer connections</li>
        <li>NAT / CGNAT traversal via relay</li>
        <li>PAKE encrypted invites</li>
        <li>Daemon mode with auto-reconnect</li>
        <li>SSH, RDP, TCP service proxying</li>
        <li>Zero-knowledge membership proofs</li>
        <li>Self-hosted relay server</li>
        <li>macOS + Linux</li>
      </ul>
    </div>
    <div class="shurli-status-column shurli-status-planned">
      <div class="shurli-status-heading shurli-status-planned">Planned</div>
      <ul class="shurli-status-list">
        <li>iOS / Android apps</li>
        <li>Windows support</li>
        <li>Plugin SDK</li>
        <li>Subnet routing</li>
      </ul>
    </div>
  </div>
</div>

<!-- ============================================================ -->
<!-- SECTION: FAQ                                                  -->
<!-- ============================================================ -->

<div class="shurli-section">
  <div class="shurli-section-icon">{{< icon name="question-mark-circle" attributes="height=28" >}}</div>
  <h2 class="shurli-section-title">Frequently asked questions</h2>

  <div class="shurli-faq-list">
    <details>
      <summary>How is this different from a VPN?</summary>
      <div class="shurli-faq-answer">
        Shurli is not a VPN. It does not create a virtual network interface or route all your traffic. Instead, it tunnels specific services (SSH, RDP, databases, web apps) directly between your devices over encrypted P2P connections. There is no vendor account, no coordination server, and no third party in the path. <a href="docs/faq/comparisons/">See detailed comparisons</a>.
      </div>
    </details>
    <details>
      <summary>Do I need to open any ports?</summary>
      <div class="shurli-faq-answer">
        No. Shurli connects through relay servers that handle NAT traversal automatically. When possible, it upgrades to a direct connection via hole-punching. You never need to touch your router or configure port forwarding.
      </div>
    </details>
    <details>
      <summary>Is my data safe?</summary>
      <div class="shurli-faq-answer">
        Relay servers never see your data. All traffic is end-to-end encrypted using libp2p's Noise protocol with Ed25519 keys. The relay only forwards opaque bytes between peers. Invite codes use PAKE-encrypted handshakes, so even the pairing process leaks nothing.
      </div>
    </details>
    <details>
      <summary>What services can I access?</summary>
      <div class="shurli-faq-answer">
        Anything that runs over TCP: SSH, remote desktop (RDP/VNC), databases, web applications, AI inference servers (Ollama, vLLM), file servers, and more. If it listens on a port, Shurli can tunnel it.
      </div>
    </details>
    <details>
      <summary>Is it production-ready?</summary>
      <div class="shurli-faq-answer">
        Shurli is experimental software. The core networking layer is tested across 5+ physical network types (CGNAT, double NAT, cellular, satellite, direct IPv6), but expect rough edges. It is not recommended for safety-critical or production workloads yet.
      </div>
    </details>
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
