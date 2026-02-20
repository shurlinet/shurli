---
title: About
---

## What is peer-up?

peer-up is a peer-to-peer networking tool that lets you connect your devices directly — through NAT, CGNAT, firewalls, and across networks — without relying on cloud services or VPN providers.

It's built for people who want to own their network infrastructure: access your home server from anywhere, connect devices across locations, and proxy any TCP service through encrypted P2P tunnels.

## Engineering Philosophy

peer-up follows a few core principles:

{{< cards >}}
  {{< card title="Self-Sovereignty First" icon="key" subtitle="Your keys, your peers, your network. No accounts to create, no services to subscribe to, no data leaving your control. The trust model is SSH-style: an authorized_keys file that you manage directly." >}}
  {{< card title="Single Binary, Zero Dependencies" icon="cube" subtitle="One file to install, one file to run. No Docker, no runtime, no package manager needed. This isn't just convenience — it's resilience. Fewer moving parts means fewer things that break." >}}
  {{< card title="Honest About Limitations" icon="eye" subtitle="When peer-up can't establish a direct connection, it tells you. When it falls back to relay, it tells you. No silent degradation, no hidden costs." >}}
  {{< card title="Docs as First-Class Deliverable" icon="book-open" link="/docs/engineering-journal/" subtitle="Every architecture decision is documented with the reasoning behind it. The Engineering Journal captures not just what was built, but why every choice was made." >}}
{{< /cards >}}

## AI-Assisted Development

{{< icon name="sparkles" attributes="height=20" >}} peer-up is built with AI assistance. The architecture, design decisions, and direction come from human judgment. AI helps with code generation, documentation, and systematic analysis.

We believe in transparency about this. The quality of the code speaks for itself — every line is reviewed, tested, and shipped with the same rigor regardless of who (or what) wrote the first draft.

## Open Source

{{< icon name="github" attributes="height=20" >}} peer-up is open source under the [MIT license](https://github.com/satindergrewal/peer-up). Contributions, questions, and feedback are welcome. File an issue or open a pull request.
