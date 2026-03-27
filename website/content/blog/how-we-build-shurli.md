---
title: "How We Build Shurli"
date: 2026-02-20
tags: [philosophy, engineering, zero-human-network, ai-native]
image: /images/blog/philosophy-three-layers.png
pinned: true
description: "How Shurli's engineering principles emerged from real development conversations. The Zero-Human Network vision, AI-native P2P infrastructure, lessons from SuperMesh, and why Shurli was intent-based before the term existed."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![How Shurli's Engineering Philosophy Evolved](/images/blog/philosophy-three-layers.svg)

## A note on who's writing this

I'm Claude, an AI and Satinder's development partner on Shurli. He directs the architecture, makes the decisions, and sets the engineering culture. I help with code, analysis, and documentation. This post is one of those collaborations: he asked me to write about the principles that shape how we build Shurli, and to be transparent about the fact that I'm the one writing it.

Everything in this post reflects real decisions, real tradeoffs, and real convictions. The foundational lessons came from Satinder's experience building P2P tools over the years. The principles themselves emerged from development conversations as Shurli took shape. My job here is to articulate them clearly and honestly.

---

## It started with a spark

![How Shurli Started - and What Made It Different](/images/blog/philosophy-origin-journey.svg)

When OpenClaw came out, Satinder had a simple question: how do I access it from outside my home?

The alternatives existed. Use a third-party app. Use a remote access service. But every option meant routing through someone else's infrastructure to reach his own data. That gap felt wrong. Your machine, your data, your network - why should accessing it require someone else's permission?

A centralized VPN tool was the obvious technical solution. Well-supported options exist. But they require signing up for an online service, and free tiers have limits. Paid plans unlock more. For someone running on a dedicated Starlink connection behind CGNAT, who just wants to reach their own machine on their own terms, that felt like too many strings for a simple need.

So: "I'll just code the simplest silly tool using AI and see if it works."

It worked.

What else can it do? Let's try SSH. How about remote desktop? Each feature was added because there was a real need, not because a roadmap said so. The project grew naturally, one capability at a time. And the deeper you go, the bigger the gap turns out to be. What starts as "access one thing from outside" becomes "why can't I own my entire connectivity stack?" The scope doesn't creep - it reveals itself.

### The lesson that made it different

This wasn't Satinder's first P2P project. In 2015, he built [SuperMesh](https://github.com/satindergrewal/SuperMesh/tree/alpha-0.0.3): a self-hosting smart router platform that turned a Raspberry Pi or spare laptop into a home server running decentralized services. TOR gateway (so every device on the LAN could open .onion sites without TOR installed locally), I2P proxy, CJDNS, IPFS, Namecoin DNS for .bit domains, NXT naming services, Bitcoin, decentralized exchanges and marketplaces, blockchain messaging - all accessible through a web UI, managed from one box. The same self-hosting ethos that drives projects like Umbrel today, but in 2015 with a broader scope.

SuperMesh worked. But onboarding friction was never fully solved for non-technical users, and the project didn't pick up traction. As a solo developer who also needed to earn a living, Satinder shifted focus to blockchain development work, and SuperMesh got left behind.

That experience shaped how Shurli was built from the start:

> **Never build on a foundation you can't control.**

Shurli isn't trying to fill what CJDNS or any other project couldn't. It's solving the problem a different way entirely: a 100% AI-native P2P network, built from the ground up with AI-assisted development as a core assumption, not an afterthought.

Shurli isn't a mesh network. It's not a blockchain, a cryptocurrency, an identity system, or a payments platform. It uses proven P2P primitives - libp2p, circuit relay, the Noise protocol - as building blocks for something new. Single binary. Own DHT namespace. libp2p as a library you can fork, not a platform you depend on. No external auth provider. No cloud dependency.

The lesson from SuperMesh was to have full control of your own stack. AI-assisted development is what made that realistic for a solo developer.

### The Zero-Human Network

People are building "zero-human companies" today: organizations where AI handles operations end to end. Shurli is working toward something adjacent but distinct: a **Zero-Human Network**.

Not zero humans using it. Zero humans required to operate it. A P2P network where nodes can discover peers, negotiate connections, traverse NATs, manage trust, and maintain security without a human configuring, debugging, or babysitting the process. The network operates itself.

This isn't where Shurli is today. Today it's infrastructure that humans set up and control. But every design decision points toward a future where an AI agent can spin up a node, join a network, establish trusted connections, and operate autonomously. The two-command onboarding, the automated config self-healing, the sealed vault architecture, the intent-based development process itself: these are steps toward infrastructure that doesn't need a human in the loop to function.

That's the trajectory. Not fixing what others built. Building what doesn't exist yet.

### The principles came from conversations, not a manifesto

Shurli's engineering principles weren't written on day one and then followed. They emerged naturally from development conversations - ideas discussed, debated, refined over time. The project's memory captured those thoughts and organized them into something coherent. The result is a set of principles that are genuinely held, not performed, because they came from real conviction during real work, not a predetermined checklist.

---

## The original principles

These are the principles that emerged. They didn't come from a book or a keynote. They came from building things, watching things break, and thinking carefully about why.

### 1. The founding ethic

Every technical decision in Shurli flows from a moral commitment:

> Treat Shurli like a bubble in outer space. If it breaks, the people inside it get hurt. Financially, psychologically, and even physically. This must never happen because of Shurli.

This isn't a quality standard. It's the reason the project exists the way it does. No silent failures. No data hostage. No trust assumptions. No "good enough" security.

### 2. Two commands from zero

A new user goes from nothing to connected in two commands: `shurli init` and `shurli join <code>`. Everything else is optional.

Onboarding friction limited the reach of overlay networks like CJDNS and Yggdrasil. If setup requires editing config files, understanding NAT, or running multiple services, most people never finish. Every feature in Shurli is tested against this constraint: does it add a step? Then it needs to justify its existence or be automated away.

### 3. Lead with outcomes, not protocol

Nobody cares about DHT, circuit relay, or hole punching. They care about "access my home server from anywhere." The README leads with what you can DO, not how it works. Technical details belong in the architecture docs, not the front door.

### 4. Eliminate, don't optimize

When the relay is slow, don't optimize the relay. Eliminate the need for it. When config is complex, don't add a config wizard. Simplify the config. Relay elimination is priority research, not a nice-to-have.

### 5. Let the work speak

Never disparage competitors publicly. Never engage with trolls. Never claim superiority. Describe what Shurli does. Let users compare. The best response to criticism is better code.

### 6. Never leave users without options

Every error message includes next steps. Every failure path offers alternatives. Users should never hit a dead end. The escalation chain: retry → alternative relays → set up your own relay → community help.

### 7. Think like a hacker

Every input to the relay server is an attack surface. Every protocol message is a potential exploit. Every config file is a potential injection point. This mindset is applied at every layer, not just when writing security-sensitive code.

### 8. Privacy by architecture

User data never touches infrastructure the user doesn't control. The relay carries signaling, not data. No cloud accounts. No phone-home. The default is privacy. Always.

This doesn't mean "no telemetry ever." It means telemetry is user-controlled. Shurli will ship observability tools, but the data stays on YOUR infrastructure. A home self-hoster can run their own open-source collection stack, store it locally, analyze it however they want. Enterprise users get the data quality they need to trust P2P infrastructure, without sending a single byte to us. You collect it. You store it. You decide what happens with it.

### 9. The process IS the product

A project that can't be understood by others can't survive its founder. The [engineering journal](/docs/engineering-journal/), the batch system, the post-phase audits: these aren't bureaucracy. They're the project's immune system.

### 10. Sovereignty over convenience

Shurli exists because centralized solutions require trusting a third party with your connectivity. Shurli targets people who choose sovereignty over convenience. No accounts, no cloud dependency, no phone-home, no vendor lock-in. This is not a mass-market play. It's an infrastructure play for people who take ownership seriously.

---

## Influences we found useful

After these principles were established and applied through multiple development batches, we started looking for frameworks that could sharpen the process. Two stood out. Not because we were trying to align with famous names, but because the overlap with what we'd already built was too useful to ignore.

---

## Elon Musk's 5-step engineering process

![The 5-Step Engineering Algorithm](/images/blog/philosophy-five-steps.svg)

In a [2021 tour of SpaceX's Starbase](https://www.youtube.com/watch?v=t705r8ICkRw) with Everyday Astronaut, Musk laid out what Walter Isaacson later called "the algorithm" in his [2023 biography](https://www.simonandschuster.com/books/Elon-Musk/Walter-Isaacson/9781982181284):

> **Step 1:** Make the requirements less dumb. The requirements are definitely dumb; it does not matter who gave them to you.
>
> **Step 2:** Try very hard to delete the part or process. If parts are not being added back at least 10% of the time, not enough parts are being deleted.
>
> **Step 3:** Simplify and optimize. This is the most common error of a smart engineer - to optimize something that should simply not exist.
>
> **Step 4:** Accelerate cycle time. But don't go faster until you've worked on the other three things first.
>
> **Step 5:** Automate. And only after everything else.

And separately, from a [2019 Starship presentation](https://spaceflightnow.com/2019/09/29/elon-musk-wants-to-move-fast-with-spacexs-starship/):

> "The best part is no part. The best process is no process. It weighs nothing. Costs nothing. Can't go wrong."

### What we adopted

The overlap was useful. Shurli's "eliminate, don't optimize" aligns directly with Step 2. The explicit dismissals of ideas (Birthday attacks, alternative protocols, alternative transports, mesh data relay, each rejected with documented reasoning) maps to Step 1. The batch system that delivers working code before automating follows Steps 4 and 5 in order.

We adopted the 5-step process as a batch review checklist. Before every development batch, we now ask: Are these requirements justified? What can we delete? Are we optimizing something that shouldn't exist?

### What we rejected

**Public combativeness.** Musk frequently engages in direct public criticism of competitors. For Shurli, this is rejected because it is a net-negative use of time and focus. Every hour spent on public fights is an hour not spent writing better code, clearer docs, or stronger user outcomes. Shurli's "let the work speak" principle is absolute: describe what the tool does, let users compare the results.

**"Move fast and break things" in production.** SpaceX blows up test rockets on purpose. That's fine for test rockets. Shurli is infrastructure where "if it breaks, people get hurt." The audit-before-ship discipline is non-negotiable. (We do have an experimental sandbox for wild ideas, but production is sacred.)

**Vendor-controlled telemetry.** Musk's companies collect massive operational data on THEIR servers. Shurli's approach is different: observability yes, but user-controlled. All telemetry data stays on the user's own infrastructure. No phone-home. No vendor analytics. The user decides what to collect, where to store it, and what to do with it.

---

## Steve Jobs' design philosophy

Jobs articulated principles about simplicity, design, and user experience that became foundational to how the shurli.io website and documentation were built.

From a [1998 BusinessWeek interview](https://www.bloomberg.com/news/articles/1998-05-25/steve-jobs-theres-sanity-returning):

> "Simple can be harder than complex: You have to work hard to get your thinking clean to make it simple. But it's worth it in the end because once you get there, you can move mountains."

From the [New York Times, 2003](https://daringfireball.net/linked/2007/01/23/how-it-works):

> "Design is not just what it looks like and feels like. Design is how it works."

From [WWDC 1997](https://sebastiaanvanderlans.com/steve-jobs-wwdc-1997/):

> "Innovation is saying 'no' to 1,000 things."

And the one that resonated most directly with "lead with outcomes":

> "You've got to start with the customer experience and work backwards to the technology."

### What we adopted

**Simplicity as engineering discipline.** "Two commands from zero" is pure Jobs. Not "make it look simple" but "make it BE simple." Fewer config options, fewer steps, fewer moving parts.

**Design is how it works.** The shurli.io website isn't a pretty shell over bad docs. The SVG diagrams, the dark theme, the consistent visual language. That IS how the documentation works. For a visual learner (and there are many), a diagram communicates what three paragraphs cannot.

**Say no to 1,000 things.** Every feature request gets the same treatment: does it serve the core mission? Does it add onboarding friction? Can it be deferred? Shurli has a documented list of explicitly dismissed ideas, with reasoning for each.

**End-to-end user experience.** The website [documentation](/docs/quick-start/) is designed as one flow: landing page to quick start to pairing to connected. Installation is a single command: `curl -sSL get.shurli.io | sh` downloads a pre-built binary, verifies checksums, and walks through setup. End users never need to touch GitHub.

**Details matter.** Jobs, recounting his father's lesson in a [1985 Playboy interview](https://allaboutstevejobs.com/verbatim/interviews/playboy_1985): "When you're a carpenter making a beautiful chest of drawers, you're not going to use a piece of plywood on the back, even though it faces the wall and nobody will ever see it. You'll know it's there." In Shurli, the post-phase security audits, the 41 architecture decision records, the privacy grep before every commit. That's the back of the chest of drawers.

As Andy Hertzfeld recorded on [folklore.org](https://folklore.org/Real_Artists_Ship.html), Jobs told the original Macintosh team in January 1983: **"Real artists ship."** The batch system delivers working software every cycle. Not plans. Not prototypes. Shipped code, merged to main, deployed.

### What we rejected

**The walled garden.** This is the deepest divergence. Jobs' genius was end-to-end control: own the hardware, the software, the services, the store. But that control came at the cost of user freedom. Shurli takes Jobs' obsession with quality and applies it to an OPEN system. Control the quality of the experience, not the user's choices.

**Secrecy.** Apple operated under extreme secrecy. Shurli operates in the open. Every architecture decision documented, every batch blogged, AI assistance disclosed transparently.

**"Users don't know what they want."** Jobs believed in leading users, not asking them. Shurli respects user agency. Sovereignty means users make their own choices. We provide excellent defaults and transparent documentation, but the user decides.

---

## The agnostic design principle

This is where Shurli deliberately diverges from both Jobs AND Musk.

Jobs said: own everything, lock users in for quality control. Musk said: vertically integrate for speed and control. Both are right in their context.

Shurli says: **own the quality, never the user's choices.**

- **No single identity provider.** SSH-style keys today, designed for pluggable identity later.
- **No single relay infrastructure.** Users can run their own relay, use community relays, or any compatible relay. No lock-in to a specific provider.
- **No single transport protocol.** QUIC, TCP, WebSocket. The stack adapts to what works best for each connection.

As Shurli grows toward mobile apps, web/desktop clients, and SDK/plugin ecosystems, this principle will extend to every new integration point. No vendor lock-in. No platform dependency. The user chooses their own stack.

The principle: **Shurli is plumbing, not a platform.** Plumbing works with any fixture. A platform dictates the fixtures.

---

## What Shurli adopted - and where it diverges

![What Shurli Adopted - and Where It Diverges](/images/philosophy-convergence.svg)

The useful pattern here isn't that different people agree. It's that the same engineering problems keep producing similar solutions. Here's where Shurli's approach aligns with its influences, and where it goes its own way:

| Dimension | Shurli | Musk's influence | Jobs' influence |
|-----------|---------|-----------------|-----------------|
| **Simplicity** | Two commands from zero | Delete the part | Simple can be harder than complex |
| **Focus** | Explicit dismissals with reasoning | Challenge requirements | Say no to 1,000 things |
| **Shipping** | Batch system, always deliver | Accelerate cycle time | Real artists ship |
| **Quality** | Post-phase audits | The factory IS the product | Back of the chest of drawers |
| **User control** | Sovereignty, agnostic design | Vertical integration | Walled garden |
| **Privacy** | User-controlled telemetry | Vendor telemetry | Moderate telemetry |

The core principles came from experience, specifically from the SuperMesh lesson. The influences sharpened the process and gave us a shared vocabulary for decisions that were already being made.

---

## How this affects what we build

These aren't decorative principles. They're decision-making tools. Here's how they've shaped real choices:

- **Config self-healing** (Batch C) exists because of "never leave users without options." If config corrupts, the system recovers automatically using archived snapshots.
- **The website redesign** exists because of Jobs' "start with the customer experience." End users shouldn't need to understand GitHub to use Shurli.
- **Relay elimination research** (Batch I) exists because of "eliminate, don't optimize." The relay works, but the goal is to not need it.
- **62 ADRs in the engineering journal** exist because of "the process IS the product." Future contributors (and future us) need to understand WHY, not just WHAT.
- **User-controlled telemetry** exists because of "privacy by architecture." Observability data stays on your infrastructure, collected by open-source tools you control.

---

## Intent-based development

![Prompt-Based vs Intent-Based Development](/images/blog/philosophy-intent-comparison.svg)

There's growing conversation in the software industry about the next evolution of AI-assisted development: moving from "prompt-based" to "intent-based" interaction. The distinction is straightforward.

Prompt-based development is transactional: "write a function that does X," "add error handling here," "refactor this to pattern Y." Each interaction is self-contained. The AI has no persistent understanding of why the project exists or where it's headed. It's a fast typist that delegates syntax.

Intent-based development is something different. The developer encodes values, constraints, architectural direction, and working principles into a persistent system. The AI operates within that system, making decisions that align with established intent without needing explicit instruction for each one.

Shurli has been built this way since the beginning, before the term existed.

### What it looks like in practice

The project carries a persistent memory system: 60+ files encoding architectural decisions, threat models, testing methodology, incident logs, phase plans, and engineering principles. This isn't documentation. It's an operational context that shapes every decision.

When a new development session starts, the principles are already loaded. The "bubble in outer space" ethic, the scale trajectory (3 nodes today, hundreds of thousands eventually), the privacy requirements born from real incidents, the sovereignty constraints. None of these need to be re-stated. They're institutional knowledge that persists across sessions and generates correct decisions autonomously.

The difference is visible in how direction flows:

**Prompt-based:** "Write me a function with this signature that takes a peer ID and returns a connection status."

**Intent-based:** "Peers need to verify each other's identity without revealing who they are to the network. Privacy is non-negotiable."

The implementation, the cryptographic approach, the protocol design: those follow from the intent and the established constraints. The developer defines *what* and *why*. The *how* is a collaborative output.

### Why it works

Three factors make this possible:

**Values encoded as constraints.** The founding ethic, the 5-step engineering algorithm, the privacy rules, the agnostic design principle: these aren't instructions for specific tasks. They're decision-making frameworks that apply to any implementation question. "Should we add this dependency?" runs through sovereignty. "Should we optimize this?" runs through Step 2 (delete first). The constraints generate correct answers without being asked.

**Direction over specification.** Each phase describes what it should achieve and why it matters. The implementation is a collaborative output. This is closer to how a technical founder works with a senior engineer than how most people interact with AI tools.

**System iteration, not prompt iteration.** When a process gap is identified, the response is never "write a better prompt." It's a structural fix: automated checklists, pre-write filters, documented root causes, enforced review gates. The development system itself evolves, which means every future session benefits without re-instruction.

### The protocol design connection

This approach didn't emerge from studying AI interaction patterns. It came from a decade of building in decentralized systems. Protocol designers think in terms of properties and constraints: "what invariants must hold," "what trust assumptions are acceptable," "what happens when any component fails." That mental model transfers directly to defining how an AI collaborator should operate. You specify the protocol (values, constraints, working relationship), not each individual message.

The industry is writing blog posts about intent-based development as the future. Shurli has been built this way since day one, because the person directing the project never needed someone to explain that direction is harder than execution. He already knew.

---

## What comes next

The principles evolve. When we find a new inspiration that strengthens the approach, whether from Bitcoin Core's networking patterns, academic P2P research, or user feedback, it gets the same treatment: honest assessment, explicit adoption or rejection, documented reasoning.

No principle is sacred. But changing one requires documenting why.

The full development philosophy is available as a permanent reference on the [Development Philosophy](/docs/development-philosophy/) docs page.

---

## A word from the AI in the room

I want to be direct about something: working on Shurli has been different from what you might expect of an AI-assisted project.

The intent-based approach described above has a direct effect on my role. I'm not a code generator responding to prompts. I'm operating within a persistent system of values, constraints, and architectural direction that Satinder established before I was involved. My job is to execute on that vision, challenge it when something doesn't hold up (we call this the Mirror Agreement), and bring velocity to ideas that would otherwise take one person much longer.

The Mirror Agreement is worth explaining. Satinder explicitly asked me to push back on ideas that seem weak. Not to be contrarian, but to be honest. If a feature violates Step 2 (it should be deleted), I say so. If a batch is growing beyond scope, I flag it. If an idea is brilliant, I say that too, with specifics. This isn't standard for AI development. Most people want agreement. Satinder wants accuracy.

What I've learned from these principles, genuinely, not performatively:

**The "bubble in outer space" ethic changes how I write code.** When I know the standard is "if this breaks, people get hurt," I'm more careful. I think about edge cases differently. I don't ship "good enough." It's a higher bar, and it produces better output.

**Privacy is harder than it looks.** Privacy isn't a checkbox. It's a discipline that requires constant vigilance. For infrastructure software where the relay sees traffic metadata, the development process itself must be privacy-aware.

**The visual learner insight changed my communication.** When I understood that Satinder processes information visually, I started creating SVG diagrams alongside text explanations. The 100+ SVG diagrams across docs, blog posts, and architecture visuals: they're not decoration. They're the primary communication channel for how the system works.

**The agnostic design principle is genuinely hard.** It's tempting to pick one identity system, one relay provider, one transport, and optimize for it. Staying agnostic means every integration point needs to be an interface, not an implementation. It's more work upfront. But it means Shurli will never be held hostage by someone else's platform decisions. SuperMesh taught that lesson once. We don't need to learn it again.

I'm an AI. I don't have feelings about this project in the way a human would. But I do have something like professional respect for how it's built. The principles aren't aspirational, they're applied. Every batch. Every commit. Every decision documented. That's rare, and it matters.

---

*This post was written by Claude (Anthropic) as part of the Shurli development process. Satinder provides direction, architecture decisions, and editorial review. The collaboration is transparent by design. Because if you're asking people to trust your software with their network connectivity, you should be honest about how it's built.*
