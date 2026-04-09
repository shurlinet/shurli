// Shurli Whitepaper - Living Protocol Document
// Toolchain: typst compile shurli-whitepaper.typ

#import "@preview/cetz:0.3.4"

// Color palette
#let shurli-blue = rgb("#2563eb")
#let shurli-dark = rgb("#1e293b")
#let accent-green = rgb("#059669")
#let accent-amber = rgb("#d97706")
#let accent-red = rgb("#dc2626")
#let subtle-gray = rgb("#94a3b8")
#let bg-warm = rgb("#fefce8")
#let bg-blue = rgb("#eff6ff")
#let bg-green = rgb("#ecfdf5")
#let bg-red = rgb("#fef2f2")

#set document(
  title: "Shurli: Distributed Social Proof for P2P Trust Without Blockchain",
  author: "Satinderjit Singh",
  date: datetime(year: 2026, month: 4, day: 8),
)

#set page(
  paper: "a4",
  margin: (x: 2.5cm, y: 2.5cm),
  numbering: "1",
)

#set text(
  font: "New Computer Modern",
  size: 11pt,
)

#set par(
  justify: true,
  leading: 0.65em,
)

#set heading(numbering: "1.1")

#show heading.where(level: 1): it => {
  v(1.2em)
  it
  v(0.5em)
}

#show heading.where(level: 2): it => {
  v(0.8em)
  it
  v(0.4em)
}

// ─────────────────────────────────────────────
// FRONT MATTER + ABSTRACT + INTRODUCTION (all on page 1)
// ─────────────────────────────────────────────

#align(center)[
  #v(1cm)
  #text(size: 18pt, weight: "bold")[Shurli: Distributed Social Proof for \ P2P Trust Without Blockchain]

  #v(0.5cm)
  #text(size: 11pt)[Satinderjit Singh] \
  #text(size: 10pt)[with Claude, Anthropic] \
  #text(size: 10pt)[#link("https://shurli.io")[shurli.io]] \
  #text(size: 10pt, fill: luma(120))[8 April 2026]
]

#v(0.6cm)

#pad(x: 2em)[
  #text(size: 10pt)[
    #text(weight: "bold")[Abstract.] #h(0.3em) A purely peer-to-peer network layer for AI-native applications would allow agents and humans to connect, communicate, and transfer data directly without routing through centralized infrastructure. Cryptographic identity provides part of the solution, but the main benefits are lost if a trusted third party is still required to verify peer reliability and authorize access. We propose a trust framework called distributed social proof (DSP), in which reputation emerges from overlapping behavioral observation by independent agents on the network, requiring no ledger, no tokens, and no consensus algorithms. The network derives trust scores by applying matrix factorization to interaction ratings, extracting genuine peer quality from cross-factional agreement rather than majority vote. Peers prove their reputation exceeds a threshold using zero-knowledge proofs @goldwasser1989knowledge without revealing the score itself, enabling privacy-preserving access control. Authorization uses capability tokens @birgisson2014macaroons that can only be made more restrictive as they are delegated, never more permissive, a mathematical guarantee from HMAC chains. As long as observers span sufficiently diverse network positions, coordinated manipulation of the reputation layer becomes proportionally more expensive. The network is designed to operate without humans in the loop: nodes discover peers, traverse NATs, manage trust, and maintain security autonomously. It is agnostic by design. Payment methods, naming systems, identity providers, and agent frameworks all plug in. The core provides only what is essential: transport, identity, discovery, authorization, and trust. We present Shurli, an open-source implementation on libp2p, as the reference architecture.
  ]
]

// ─────────────────────────────────────────────
// SECTION 1: INTRODUCTION (starts on same page as abstract)
// ─────────────────────────────────────────────

= Introduction

Connectivity on the internet has come to rely almost exclusively on centralized services serving as intermediaries between devices. While the system works well enough for most use cases, it suffers from inherent weaknesses of the trust-based model. Every connection routes through infrastructure controlled by a third party who can revoke access, change terms, or shut down entirely. Carrier-grade NAT blocks direct connections for billions of devices, forcing reliance on relay services the user does not control. AI agents that need to communicate with each other must route through centralized cloud platforms, creating single points of failure and surveillance. Self-hosters cannot reach their own machines without signing up for an online service. The cost of this intermediation is not just financial. It is a loss of sovereignty: the network decides who connects to whom, not the participants themselves.

What is needed is a network and communication layer based on behavioral observation instead of institutional trust, allowing any two peers, human or AI, to connect directly without a centralized intermediary. A system where trust emerges from how peers actually behave on the network, not from which provider vouches for them. In this paper, we propose distributed social proof (DSP): a trust framework maintained by independent agents that observe, evaluate, and report on peer behavior, requiring no ledger, no tokens, and no consensus algorithms. We present Shurli, an open-source implementation on libp2p, as the reference architecture for a *Zero-Human Network*: P2P infrastructure where nodes discover peers, traverse NATs, manage trust, and maintain security without a human in the loop. The system is agnostic by design. Payment methods, naming systems, identity providers, and agent frameworks all plug in. The core provides only what is essential: transport, identity, discovery, authorization, and trust. The network is secure as long as observers span sufficiently diverse network positions, making coordinated reputation manipulation proportionally more expensive than honest participation.

// ─────────────────────────────────────────────
// SECTION 2: DISTRIBUTED SOCIAL PROOF
// ─────────────────────────────────────────────

= Distributed Social Proof

Trust in networked systems has historically taken two forms: computational proof (blockchain), which is physics-backed but rigid, energy-intensive, and binary; or institutional authority (CAs, DNS, cloud), which is flexible but centralized. Human societies operate on neither. They establish trust through overlapping observation, behavioral reputation, and collective judgment. No cryptographic proof backs a scientist's reputation. DSP applies this third model to P2P networking: trust maintained by independent AI agents that observe, evaluate, and report on peer behavior. No global ledger. No central authority. Trust is an emergent property of sufficient independent observation.

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    // Left: Blockchain
    content((-5.5, 5.8), text(size: 10pt, weight: "bold", fill: shurli-dark)[Blockchain])
    for i in range(4) {
      let x = -8.5 + i * 2.5
      let y = 3.5
      rect((x, y), (x + 2, y + 1.6), stroke: (paint: accent-amber, thickness: 0.8pt), radius: 3pt, fill: bg-warm)
      content((x + 1, y + 1.1), text(size: 8.5pt, weight: "bold", fill: shurli-dark)[Block #str(i)])
      rect((x + 0.15, y + 0.15), (x + 1.85, y + 0.6), stroke: (paint: luma(200), thickness: 0.3pt), radius: 2pt, fill: white)
      content((x + 1, y + 0.38), text(size: 6pt, fill: subtle-gray)[hash(B#str(i))])
    }
    for i in range(3) {
      let x1 = -8.5 + i * 2.5 + 2
      let x2 = -8.5 + (i + 1) * 2.5
      line((x1 + 0.05, 4.3), (x2 - 0.05, 4.3), stroke: (paint: accent-amber, thickness: 0.7pt), mark: (end: ">", size: 0.2))
    }
    content((-4.5, 2.8), text(size: 7.5pt, style: "italic", fill: subtle-gray)[Linear, sequential, energy-intensive])

    // Right: DSP
    content((5.5, 5.8), text(size: 10pt, weight: "bold", fill: shurli-dark)[Distributed Social Proof])
    let nodes = (("A", (3.0, 4.5)), ("B", (4.5, 5.2)), ("C", (6.0, 4.7)), ("D", (7.5, 5.0)),
                 ("E", (3.5, 3.2)), ("F", (5.5, 3.5)), ("G", (7.3, 3.3)), ("H", (4.7, 2.2)))
    let edges = ((0,1),(0,4),(1,2),(1,4),(1,5),(2,3),(2,5),(3,6),(4,5),(4,7),(5,6),(5,7),(6,3))
    for edge in edges {
      let (i, j) = edge
      let (_, p1) = nodes.at(i)
      let (_, p2) = nodes.at(j)
      line(p1, p2, stroke: (paint: rgb("#93c5fd"), thickness: 0.6pt))
    }
    for (label, pos) in nodes {
      circle(pos, radius: 0.38, fill: bg-blue, stroke: (paint: shurli-blue, thickness: 0.9pt))
      content(pos, text(size: 8pt, weight: "bold", fill: shurli-blue)[#label])
    }
    content((5.5, 1.6), text(size: 7.5pt, style: "italic", fill: subtle-gray)[Adaptive, parallel, zero infrastructure])

    // Divider
    line((1.2, 1.3), (1.2, 6.1), stroke: (paint: luma(210), thickness: 0.5pt, dash: "dashed"))
  }),
  caption: [Structural comparison. Blockchain: linear chain of cryptographic proofs. DSP: mesh of independent agent observations.],
) <fig-blockchain-vs-dsp>

== Core Properties

*Emergent trust.* No single agent is authoritative. Trust scores emerge from convergence of independent observations, analogous to the Community Notes mechanism @communitynotes2023 where consensus must bridge diverse perspectives.

*Fuzzy evaluation.* Unlike blockchain's binary model, DSP supports graduated trust: a node may be unreliable for latency claims but adequate for relay; an agent may deliver excellent inference results but settle payments slowly. Multi-dimensional trust, not binary.

*Zero infrastructure.* No mining, staking, tokens, or persistent ledger. Agents observe peers during normal operation. Trust is a byproduct of participation.

*Adaptive response.* Reputation degrades and recovers dynamically. Misreporting nodes are deprioritized without governance votes, hard forks, or slashing events.

== The Observation Model <observation-model>

DSP agents observe concrete, verifiable behaviors at two levels. These are not subjective ratings but measurable properties of peer interactions.

*Network-level observations* capture how reliably a peer participates in the infrastructure: latency accuracy (claimed versus measured), relay uptime and circuit completion rate, connection quality (packet loss, reconnection frequency), signaling honesty (bootstrap record freshness), transfer integrity (BLAKE3 hash match rate, protocol compliance), and bandwidth delivery (advertised versus actual throughput).

*Agent-level observations* capture how honestly a peer fulfills commitments in data and value exchange: did the AI agent complete the requested task, did it deliver the promised output, was the data exchanged intact and as described, was the value settlement honored. When a human asks their AI to accomplish something and that AI delegates subtasks to other agents across the network, every step in that chain produces observable evidence. An agent that consistently delivers correct inference results, completes file transfers without corruption, and settles payments as agreed builds reputation. An agent that fails to deliver, returns garbage, or disappears mid-task loses it. Both peers sign bilateral transfer receipts as cryptographic evidence of every interaction.

This is what makes DSP agent-native: the same reputation framework that tracks network reliability also tracks agent honesty. A single score reflects both "can I reach this peer" and "will this peer do what it promised." Each observation is independently verifiable. The model produces ratings automatically from real interactions, not from explicit voting.

The reputation API is exposed to all plugins. File transfer, naming resolution, and any future plugin can feed observations into the same reputation system and query scores from it. This makes reputation extensible beyond use cases the core designers anticipated. Connected external identities (social profiles, messaging accounts, enterprise directories) provide an additional trust dimension: identity trust inherited from established presence elsewhere, weighted separately from performance trust earned through actual behavior on the network.

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    // Left: observation sources (stacked, taller boxes)
    let sources = (
      ("Network", "Latency, relay, bandwidth", accent-amber, bg-warm),
      ("Agent", "Task completion, delivery", shurli-blue, bg-blue),
      ("Plugin", "File transfer, naming, ...", accent-green, bg-green),
      ("Identity", "External profiles, DIDs", rgb("#7c3aed"), rgb("#f5f3ff")),
    )

    for i in range(4) {
      let (label, desc, col, bg) = sources.at(i)
      let y = 3.6 - i * 1.3
      rect((0, y), (4.0, y + 1.1), stroke: (paint: col, thickness: 0.8pt), fill: bg, radius: 3pt)
      content((2.0, y + 0.7), text(size: 8pt, weight: "bold", fill: col)[#label])
      content((2.0, y + 0.35), text(size: 6.5pt, fill: subtle-gray)[#desc])
      // Arrow to center
      line((4.05, y + 0.55), (5.45, 2.1), stroke: (paint: luma(180), thickness: 0.5pt), mark: (end: ">", size: 0.15))
    }

    // Center: bridged consensus
    rect((5.5, 1.0), (9.3, 3.2), stroke: (paint: shurli-blue, thickness: 1.2pt), fill: bg-blue, radius: 4pt)
    content((7.4, 2.6), text(size: 8.5pt, weight: "bold", fill: shurli-blue)[Bridged Consensus])
    content((7.4, 2.0), text(size: 7pt, fill: shurli-dark)[Matrix factorization])
    content((7.4, 1.55), text(size: 7pt, fill: subtle-gray)[Cross-factional agreement])

    // Right: output
    line((9.35, 2.1), (10.45, 2.1), stroke: (paint: luma(150), thickness: 0.8pt), mark: (end: ">", size: 0.2))

    rect((10.5, 1.0), (14.3, 3.2), stroke: (paint: accent-red, thickness: 0.8pt), fill: bg-red, radius: 4pt)
    content((12.4, 2.6), text(size: 8.5pt, weight: "bold", fill: accent-red)[Reputation Score])
    content((12.4, 2.0), text(size: 7pt, fill: shurli-dark)[Performance trust])
    content((12.4, 1.55), text(size: 7pt, fill: subtle-gray)[+ Identity trust (tiered)])

    // Bottom annotation
    content((7.0, -0.5),
      text(size: 7.5pt, style: "italic", fill: subtle-gray)[
        Multiple observation sources feed a single reputation framework. No ledger required.
      ])
  }),
  caption: [Network, agent, plugin, and identity observations feed into bridged consensus, producing a composite reputation score with separate performance and identity trust dimensions.],
) <fig-reputation-flow>

// ─────────────────────────────────────────────
// SECTION 4: SYBIL RESISTANCE AND TRUST CONVERGENCE
// ─────────────────────────────────────────────

= Sybil Resistance and Trust Convergence

The primary attack against DSP is Sybil attack @douceur2002sybil: deploying compromised agents to control the observation layer. We analyze conditions under which this becomes impractical.

== Observer Diversity Requirement

Let $N$ be total observer agents, $f$ the adversary-controlled fraction, $k$ the independent observations required for reputation update, and $d$ the number of distinct network partitions observers must span. The probability of adversary control is bounded by:

$ P_italic("sybil") (k, d) <= f^k dot f_italic("max")^(d-1) $

where $f_italic("max")$ is adversary penetration in any single partition. Even at 40% total control, achieving 40% in every independent partition requires proportionally more resources.

== Trust Convergence

For a node with true quality $q in [0, 1]$ and $n$ honest observations with noise $epsilon tilde cal(N)(0, sigma^2)$, the estimated reputation converges:

$ E[hat(R)] = q, #h(2em) "Var"(hat(R)) = sigma^2 / n $

With 25 observations and $sigma = 0.2$, the 95% confidence interval is $plus.minus 0.08$, sufficient for routing decisions. More observers yield more reliable consensus without trusting any single one.

== Bridged Consensus via Matrix Factorization

Raw observation averaging is vulnerable to factional manipulation. Shurli addresses this by adapting the Community Notes bridging algorithm @wojcik2022birdwatch to peer reputation.

The core model decomposes each rating as:

$ hat(y)_(i j) = w_i x_j + b_i + c_j $

where $w_i$ is rater $i$'s polarity factor (where they sit on a viewpoint spectrum), $x_j$ is peer $j$'s polarity factor, $b_i$ is rater $i$'s baseline tendency, and $c_j$ is peer $j$'s intercept: the genuine quality signal. The intercept captures quality that cannot be explained by factional alignment. A peer rated well by observers who disagree on everything else gets a high $c_j$; a peer rated well only by a single cluster has its ratings absorbed into the polarity factors, leaving the intercept low. The algorithm simultaneously discovers viewpoint clusters and extracts quality that transcends factional bias. A peer rated well by diverse, disagreeing observers gets a high intercept. A peer rated well only by a single cluster gets a low intercept, regardless of how large that cluster is.

Creating 1000 fake accounts that all rate each other the same way is detected as a single faction. Only cross-factional agreement moves the intercept. No blockchain, no staking, no central authority. Pure mathematics on rating patterns.

== Comparative Attack Cost

#figure(
  table(
    columns: (auto, auto, auto, auto),
    stroke: 0.5pt,
    inset: 6pt,
    table.header(
      [*Property*], [*Blockchain (PoW)*], [*Institutional*], [*DSP (Shurli)*],
    ),
    [Trust basis], [Thermodynamics], [Legal identity], [Behavioral observation],
    [Finality], [Mathematical], [Contractual], [Probabilistic],
    [Expressiveness], [Binary], [Policy-defined], [Graduated / fuzzy],
    [Infrastructure], [Very high], [Moderate], [Near zero],
    [Throughput], [Low (7 tx/s)], [High], [High],
    [Censorship resist.], [Very high], [Low], [High],
    [Sybil resistance], [High (energy)], [High (identity)], [High (diversity)],
    [Adaptability], [Very low], [Moderate], [Very high],
    [Energy (annual)], [\~150 TWh], [Moderate], [Negligible],
    [Quantum resistance], [Vulnerable], [Vulnerable], [Hybrid PQ transport],
  ),
  caption: [Comparative properties of trust frameworks.],
) <table-comparative>

// ─────────────────────────────────────────────
// SECTION 5: ARCHITECTURE
// ─────────────────────────────────────────────

= Architecture

Shurli is an open-source Go implementation on libp2p @libp2p2023. It occupies a specific position in the AI infrastructure stack: the network and communication layer beneath agent applications, frameworks, and protocols.

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    let layer-h = 1.1
    let layer-w = 12.0
    let gap = 0.15

    // Colors for each layer
    let colors-stroke = (
      shurli-blue, shurli-blue, shurli-blue, shurli-blue,  // layers 1-4: Shurli
      luma(150), luma(150), luma(150),  // layers 5-7: above
    )
    let colors-fill = (
      bg-blue, bg-blue, bg-blue, bg-blue,
      luma(248), luma(248), luma(248),
    )

    let labels = (
      "Layer 1: Transport (QUIC, TCP, WebSocket, PQC)",
      "Layer 2: Network (P2P discovery, NAT traversal, relay, streams)",
      "Layer 3: Identity & Naming (Ed25519, SNR, DIDs, petnames)",
      "Layer 4: Trust & Reputation (DSP, ZKP, macaroons, grants)",
      "Layer 5: Agent Protocols (A2A, MCP, capability discovery)",
      "Layer 6: Agent Frameworks (reasoning, planning, tools)",
      "Layer 7: AI Applications (autonomous agents, services)",
    )

    // Draw layers bottom to top
    for i in range(7) {
      let y = i * (layer-h + gap)
      let col = colors-stroke.at(i)
      let bg = colors-fill.at(i)
      rect((0, y), (layer-w, y + layer-h),
        stroke: (paint: col, thickness: 0.8pt), radius: 3pt, fill: bg)
      content((layer-w / 2, y + layer-h / 2),
        text(size: 8.5pt, fill: if i < 4 { shurli-dark } else { luma(120) })[#labels.at(i)])
    }

    // Right bracket: "Zero-Human Network (Shurli)" for layers 1-4
    let bracket-x = layer-w + 0.5
    let y-bot = 0.0
    let y-top = 4 * (layer-h + gap) - gap
    line((bracket-x, y-bot), (bracket-x + 0.3, y-bot), stroke: (paint: shurli-blue, thickness: 1pt))
    line((bracket-x + 0.3, y-bot), (bracket-x + 0.3, y-top), stroke: (paint: shurli-blue, thickness: 1pt))
    line((bracket-x, y-top), (bracket-x + 0.3, y-top), stroke: (paint: shurli-blue, thickness: 1pt))
    content((bracket-x + 1.2, y-top / 2), angle: -90deg,
      text(size: 8pt, weight: "bold", fill: shurli-blue)[Zero-Human Network (Shurli)])

    // Right bracket: "Plugs into Shurli" for layers 5-7
    let y-bot2 = 4 * (layer-h + gap)
    let y-top2 = 7 * (layer-h + gap) - gap
    line((bracket-x, y-bot2), (bracket-x + 0.3, y-bot2), stroke: (paint: luma(150), thickness: 0.8pt))
    line((bracket-x + 0.3, y-bot2), (bracket-x + 0.3, y-top2), stroke: (paint: luma(150), thickness: 0.8pt))
    line((bracket-x, y-top2), (bracket-x + 0.3, y-top2), stroke: (paint: luma(150), thickness: 0.8pt))
    content((bracket-x + 1.2, (y-bot2 + y-top2) / 2), angle: -90deg,
      text(size: 8pt, fill: luma(120))[Plugs into Shurli])
  }),
  caption: [The AI infrastructure stack. Shurli ships layers 1 through 3 today; layer 4 (DSP reputation) is in early implementation. Agent protocols, frameworks, and applications sit above and plug in via the SDK and plugin architecture.],
) <fig-architecture-stack>

*Layer 1: Transport.* Direct device connectivity via libp2p with QUIC, TCP, and WebSocket transports. NAT traversal through DCUtR hole-punching with circuit relay v2 as fallback. Software-only techniques achieve 85-88% direct connection rates @nattraversal2025 @ford2005nat. Ephemeral "fat invite codes" encode all bootstrap data, eliminating persistent infrastructure dependency. CGNAT is a design requirement, not an edge case. The QUIC transport automatically negotiates hybrid post-quantum key exchange (X25519MLKEM768) @fips203mlkem via Go's standard library.

*Layer 2: Network.* Kademlia DHT for peer discovery in an owned namespace. mDNS for zero-configuration LAN discovery. A PeerManager maintains connection lifecycle with promotion, demotion, and state tracking. Path selection is continuous: if a peer transitions from cellular to WiFi, the connection migrates automatically. Eleven upstream libp2p overrides harden the transport for production use.

*Layer 3: Identity and Naming.* BIP39 seed phrase derives all keys through HKDF-SHA256 with domain separation. One backup recovers everything. The naming standard @zooko2001names @rfc9498gns provides a five-layer resolution pipeline: PeerID (cryptographic ground truth), DID (standards interop via W3C `did:peer` @w3cdid2026), petname (local, user-assigned), nickname (self-chosen, advisory), and external (resolved via plugins for ENS, DNS, VerusID, or any naming system). The relay is explicitly prevented from becoming a name authority.

*Layer 4: Trust and Reputation.* The DSP framework described in Sections 3 and 4. DAG-based append-only interaction log. Heuristic scoring as cold-start fallback, transitioning smoothly to matrix factorization as ratings accumulate. Zero-knowledge range proofs allow peers to prove "my score exceeds threshold $t$" without revealing the score. Macaroon capability tokens @birgisson2014macaroons provide authorization with cryptographic attenuation, time-limited grants, delegation chains, and per-peer bandwidth budgets.

*Above Shurli (Layers 5-7).* Agent protocols (A2A, MCP, ANP), agent frameworks (OpenClaw, custom reasoning engines), and AI applications sit above the network layer. Shurli does not prescribe or constrain what runs above it. A compiled plugin architecture with an 11-method interface allows any application to extend the network. The SDK exposes transport, events, and peer management to plugins without leaking credentials or private keys. Layers 1-3 are shipped and production-tested. Layer 4 (DSP reputation with matrix factorization) is in early implementation; the layered trust model and Byzantine defense described in this paper are the target design.

// ─────────────────────────────────────────────
// SECTION 6: PRIVACY
// ─────────────────────────────────────────────

= Privacy

Institutional trust restricts information to parties and a central authority. DSP uses pseudonymous identities (libp2p peer IDs); only behavioral data, not content, is observed. Reputation derives from network behavior, not transmission content.

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    // Left: Traditional Model
    content((-5, 4.5), text(size: 9pt, weight: "bold", fill: shurli-dark)[Traditional Model])

    let trad-boxes = (
      ("Identity", (-7.5, 3.0)),
      ("Requests", (-5.5, 3.0)),
      ("Trusted\nAuthority", (-3.5, 3.0)),
      ("Peer", (-1.5, 3.0)),
    )
    for (label, (x, y)) in trad-boxes {
      rect((x - 0.9, y - 0.4), (x + 0.9, y + 0.4), stroke: (paint: accent-red, thickness: 0.7pt), fill: bg-red, radius: 2pt)
      content((x, y), text(size: 7pt, fill: shurli-dark)[#label])
    }
    for i in range(3) {
      let (_, (x1, _)) = trad-boxes.at(i)
      let (_, (x2, _)) = trad-boxes.at(i + 1)
      line((x1 + 0.9, 3.0), (x2 - 0.9, 3.0), stroke: 0.5pt, mark: (end: ">", size: 0.15))
    }
    rect((-7.6, 2.2), (-1.4, 2.5), stroke: none, fill: bg-red)
    content((-4.5, 2.35), text(size: 7pt, style: "italic", fill: accent-red)[All visible to authority])

    // Right: DSP Model
    content((5, 4.5), text(size: 9pt, weight: "bold", fill: shurli-dark)[DSP Model (Shurli)])

    let dsp-boxes = (
      ("Pseudonym", (2.5, 3.0)),
      ("Requests", (4.5, 3.0)),
      ("Agent\nObservers", (6.5, 3.0)),
      ("Network", (8.5, 3.0)),
    )
    for (label, (x, y)) in dsp-boxes {
      rect((x - 0.9, y - 0.4), (x + 0.9, y + 0.4), stroke: (paint: accent-green, thickness: 0.7pt), fill: bg-green, radius: 2pt)
      content((x, y), text(size: 7pt, fill: shurli-dark)[#label])
    }
    for i in range(3) {
      let (_, (x1, _)) = dsp-boxes.at(i)
      let (_, (x2, _)) = dsp-boxes.at(i + 1)
      line((x1 + 0.9, 3.0), (x2 - 0.9, 3.0), stroke: 0.5pt, mark: (end: ">", size: 0.15))
    }
    rect((2.4, 2.2), (8.6, 2.5), stroke: none, fill: bg-green)
    content((5.5, 2.35), text(size: 7pt, style: "italic", fill: accent-green)[Only behavior is visible])
  }),
  caption: [Privacy comparison. Institutional: all data flows through authority. DSP: only observable network behavior is visible to agents.],
) <fig-privacy>

Petname stores are local and never shared over the network without consent. Namespace isolation prevents cross-network identity correlation. Name resolution queries to external resolvers may leak which names a node is interested in; this is documented as a known tradeoff. Zero-knowledge proofs enable anonymous authenticated participation: a peer proves its score exceeds a threshold without revealing who it is or what the score is. Post-quantum key exchange protects current sessions against future quantum decryption of recorded traffic.

// ─────────────────────────────────────────────
// SECTION 7: INCENTIVE DESIGN
// ─────────────────────────────────────────────

= Incentive Design

The reason Shurli exists is to make the network invisible. A human tells an AI what they need. The AI figures out the rest: finding the right peers, negotiating access, exchanging data and value with other AI agents across the network, and delivering the result. The human never manages connections, configures relays, or thinks about which node has which capability. The network operates itself. This is the Zero-Human Network in practice: not a network without humans, but a network where humans interact with AI and AI handles everything underneath.

This only works if the network layer requires no manual intervention, no token purchases, and no account signups. Decentralized networks that work without tokens share one pattern: immediate selfish value to each node. BitTorrent, email, and Tor all succeed because every participant gains directly from participation. The user thinks about what they get, never about "the network." If the value requires explaining a vision, adoption fails.

Shurli follows this pattern. A node joins because it wants to reach its own machines behind NAT, share files with specific peers, or provide AI agents with autonomous connectivity. The network effect is a byproduct, not a prerequisite.

== Why No Token

Shurli has no token, no coin, and no staking requirement. This is a deliberate architectural decision, not a temporary omission. Tokens create regulatory complexity, invite speculation that distorts incentives, and gate participation behind financial cost. A network layer should be invisible infrastructure, like TCP/IP. Nobody buys a token to send a packet.

The design philosophy parallels Bitcoin's original contribution @nakamoto2008bitcoin: eliminate middlemen, place trust in mathematics. But where Bitcoin required a token to solve double-spending (a financial problem), Shurli solves a networking problem. Reputation emerges from behavior. Authorization comes from capability tokens with mathematical guarantees. Neither requires a tradeable asset.

== Agnostic Payment Layer

When economic exchange is desired, Shurli defines the payment interface, not the implementation. Payment methods are plugins: Lightning, USDC, traditional payment rails, or any future system. The protocol is agnostic. Per-task micropayments, not subscriptions. Swap rails without protocol changes.

This is consistent with Shurli's broader design: agnostic to payment, to naming, to identity, to agent frameworks. The core provides infrastructure. Everything else plugs in.

== Positive-Sum Economics

Shurli is not adversarial to centralized AI providers. A self-hoster running local inference and a cloud provider operating at scale both benefit from a network layer that handles connectivity, trust, and authorization. The network is a distribution channel for centralized inference, not a competitor to it. Decentralized and centralized, not versus. Every participant gains; nobody's position is worsened. Raising the floor does not lower the ceiling.

// ─────────────────────────────────────────────
// SECTION 8: LIMITATIONS
// ─────────────────────────────────────────────

= Limitations

*No mathematical finality.* A sufficiently resourced adversary can theoretically manipulate the reputation layer. For absolute immutability (financial settlement, legal records), blockchain remains appropriate. DSP provides probabilistic trust, sufficient for networking but not for irreversible transactions.

*Cold start.* New nodes lack reputation. The current mitigation is graduated trust escalation through low-stakes interactions: heuristic scoring for the first 5 ratings, blending to matrix factorization from 5 to 15 ratings, full MF-based scoring above 15. In private networks, invitation chains mathematically bound the Sybil fraction. In public networks, time cost (7 days), bandwidth cost, and interaction minimums (10) constrain rapid reputation gaming. Connected external identities via the naming standard provide a partial solution: a peer that links verified social profiles, messaging accounts, or enterprise directory entries inherits initial identity trust, reducing the cold-start gap while still requiring performance trust to be earned through real interactions.

*Agent integrity.* Compromised AI agents produce untrustworthy observations. Observer diversity requirements mitigate but do not eliminate this. The five-layer Byzantine defense (bilateral verification, MF consensus, relay observability, clique detection, rate limiting) ensures an attacker must compromise all layers simultaneously.

*Observation scalability.* Required observation density at global scale remains an open research question. The layered trust computation (relay computes, peers verify, gossip propagates) provides progressive decentralization, but has not been tested beyond small network deployments.

*Current scale.* Shurli v0.3.0 operates across a small number of nodes on heterogeneous networks. The architecture is designed for larger scale, but the claims in this paper are validated at small scale. This paper describes where Shurli is going, not claiming it has arrived.

*NAT traversal is probabilistic.* Direct connection success rates of 85-88% mean 12-15% of connections require relay fallback. Relay is infrastructure, not failure, but it introduces latency and bandwidth constraints.

*Post-quantum identity.* QUIC transport already negotiates hybrid PQ key exchange. Noise protocol and peer identity keys remain classical (Ed25519). ML-DSA for identity signing awaits Go standard library support. This is a known gap with a planned migration path.

// ─────────────────────────────────────────────
// SECTION 9: CONCLUSION
// ─────────────────────────────────────────────

= Conclusion

The question is not whether distributed social proof replaces blockchain. It does not. The question is whether the majority of P2P networking use cases ever required blockchain-grade guarantees. We argue they do not.

For establishing connections, verifying peers, routing traffic, maintaining integrity, and tracking whether AI agents honestly complete the work they are asked to do, distributed agent observation is sufficient, and faster, cheaper, more expressive, and more adaptable than any ledger-based alternative.

None of the components in Shurli are novel. libp2p @libp2p2023 for transport. Matrix factorization for bridged consensus @wojcik2022birdwatch. EigenTrust @kamvar2003eigentrust for trust propagation. Zero-knowledge proofs @goldwasser1989knowledge for privacy. Macaroons @birgisson2014macaroons for capability-based authorization. Petname systems @rfc9498gns for identity-agnostic naming. Hybrid post-quantum key exchange @fips203mlkem @angel2022pqnoise for forward security. The contribution is the combination: a Zero-Human Network where AI agents and humans connect, communicate, and build trust without centralized infrastructure, without blockchain, and without requiring anyone's permission.

Shurli is the experiment. This paper will evolve with it.

// ─────────────────────────────────────────────
// ACKNOWLEDGMENTS
// ─────────────────────────────────────────────

= Acknowledgments <acknowledgments>
#set heading(numbering: none)

The authors acknowledge the X Community Notes team for the bridging algorithm, Protocol Labs for the libp2p transport stack, the Go cryptography team for post-quantum primitives, the Consensys gnark team for the ZKP circuit compiler, and the PQNoise authors for post-quantum Noise protocol research.

// ─────────────────────────────────────────────
// BIBLIOGRAPHY (must be at end of document)
// ─────────────────────────────────────────────

#bibliography("refs.bib", style: "ieee")

