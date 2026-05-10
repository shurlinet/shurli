---
title: About
description: "Shurli is sovereign P2P infrastructure. Connect your devices directly through NAT, CGNAT, and firewalls. No accounts, no cloud, no central authority."
---

## What is Shurli?

Shurli is sovereign peer-to-peer infrastructure. It connects your devices directly through NAT, CGNAT, firewalls, and across networks, without relying on cloud services or vendor accounts. Your keys, your peers, your network.

## Where this is going

The long-term trajectory is the [Zero-Human Network](/blog/how-we-build-shurli/#the-zero-human-network): a network where zero humans are required to *operate* it. Not zero humans using it. Zero humans needed to keep it running. Nodes discover peers, negotiate connections, traverse NATs, and manage trust autonomously.

This is not where Shurli is today. Today it is sovereign infrastructure that humans set up and control. Every design decision points toward that trajectory.

## Engineering Philosophy

Shurli follows a few core principles. For the full story, including how these principles evolved and their intellectual lineage, see the [Development Philosophy](/docs/development-philosophy/) page and the [blog post on how we build](/blog/how-we-build-shurli/).

{{< cards >}}
  {{< card title="Self-Sovereignty First" icon="key" subtitle="Your keys, your peers, your network. No accounts to create, no services to subscribe to, no data leaving your control. The trust model is SSH-style: an authorized_keys file that you manage directly." >}}
  {{< card title="Single Binary, Zero Dependencies" icon="cube" subtitle="One file to install, one file to run. No Docker, no runtime, no package manager needed. This isn't just convenience - it's resilience. Fewer moving parts means fewer things that break." >}}
  {{< card title="Honest About Limitations" icon="eye" subtitle="When Shurli can't establish a direct connection, it tells you. When it falls back to relay, it tells you. No silent degradation, no hidden costs." >}}
  {{< card title="Docs as First-Class Deliverable" icon="book-open" link="/docs/engineering-journal/" subtitle="Every architecture decision is documented with the reasoning behind it. The Engineering Journal captures not just what was built, but why every choice was made." >}}
{{< /cards >}}

## Built with AI

{{< icon name="sparkles" attributes="height=20" >}} Shurli is not a project that bolted on AI tooling. Architecture, code, documentation, and testing are all developed with AI as a core part of the process from day one.

The direction and decisions are human. The execution leverages AI at every layer. Every line is reviewed, tested, and shipped with the same rigor regardless of origin. The code speaks for itself.

## Open Source

{{< icon name="github" attributes="height=20" >}} Shurli is open source under the [MIT license](https://github.com/shurlinet/shurli). Contributions, questions, and feedback are welcome. File an issue or open a pull request.
