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
  date: datetime(year: 2026, month: 4, day: 12),
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
  #text(size: 10pt, fill: luma(120))[v0.3.1 -- 12 April 2026]
]

#v(0.6cm)

#pad(x: 2em)[
  #text(size: 10pt)[
    #text(weight: "bold")[Abstract.] #h(0.3em) A purely peer-to-peer network layer for AI-native applications would allow agents and humans to connect, communicate, and transfer data directly without routing through centralized infrastructure. Cryptographic identity provides part of the solution, but the main benefits are lost if a trusted third party is still required to verify peer reliability and authorize access. We propose a trust framework called distributed social proof (DSP), in which reputation emerges from overlapping behavioral observation by independent agents on the network, requiring no ledger, no tokens, and no consensus algorithms. The network derives trust scores by adapting the bridging algorithm from X's Community Notes @wojcik2022birdwatch to peer reputation: matrix factorization applied to interaction ratings extracts genuine peer quality from cross-factional agreement rather than majority vote, without requiring a central operator. Peers prove their reputation exceeds a threshold using zero-knowledge proofs @goldwasser1989knowledge without revealing the score itself, enabling privacy-preserving access control. Authorization uses capability tokens @birgisson2014macaroons that can only be made more restrictive as they are delegated, never more permissive, a mathematical guarantee from HMAC chains. As long as observers span sufficiently diverse network positions, coordinated manipulation of the reputation layer becomes proportionally more expensive. The network is designed to operate without humans in the loop: nodes discover peers, traverse NATs, manage trust, and maintain security autonomously. It is agnostic by design. Payment methods, naming systems, identity providers, and agent frameworks all plug in. The core provides only what is essential: transport, identity, discovery, authorization, and trust. We present Shurli, an open-source implementation on libp2p, as the reference architecture.
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

A "network partition" in this context does not refer to geographic region or IP subnet. It refers to an _independent observation group_: a set of peers whose rating behavior is statistically independent of other groups. The matrix factorization algorithm (Section 3.3) discovers these groups automatically from rating patterns via polarity factors. Two peers controlled by the same operator, regardless of how many distinct IP addresses or VPN endpoints they use, will rate other peers the same way and cluster into the same polarity factor. The variable $d$ counts how many such _behaviorally independent_ groups must be penetrated.

This is the key distinction from IP-based identity. Bitcoin's whitepaper @nakamoto2008bitcoin observes that "if the majority were based on one-IP-address-one-vote, it could be subverted by anyone able to allocate many IPs." DSP does not use IP-based identity at all. A single entity operating 10,000 nodes behind 10,000 different IP addresses still constitutes a single faction in the MF decomposition, because behavioral patterns, not network addresses, determine partition membership. The cost to the attacker is not "acquire many IPs" but "get your fake nodes rated well by nodes that disagree with each other on everything else," which requires either compromising genuinely independent peers or building real cross-factional reputation through honest participation.

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    // Title
    content((3.5, 6.8), text(size: 9pt, weight: "bold", fill: shurli-dark)[IP-Based Identity])
    content((11.5, 6.8), text(size: 9pt, weight: "bold", fill: shurli-dark)[Rating-Pattern Identity (DSP)])

    // Left side: IP-based (defeated)
    // Attacker with many IPs
    let attacker-col = accent-red
    for i in range(6) {
      let x = 1.0 + calc.rem(i, 3) * 1.8
      let y = 4.8 - calc.floor(i / 3) * 1.8
      circle((x, y), radius: 0.45, fill: bg-red, stroke: (paint: attacker-col, thickness: 0.8pt))
      content((x, y + 0.05), text(size: 6pt, fill: attacker-col)[IP-#str(i + 1)])
    }
    // Bracket: "1 entity"
    content((3.5, 2.2), text(size: 7pt, style: "italic", fill: attacker-col)[6 IPs, 1 entity])
    // Check mark: defeats IP voting
    content((3.5, 1.5), text(size: 7pt, fill: attacker-col)[6 votes in IP-based system])

    // Divider
    line((7.2, 1.0), (7.2, 6.5), stroke: (paint: luma(210), thickness: 0.5pt, dash: "dashed"))

    // Right side: MF-based (detected)
    // Same 6 nodes, but MF clusters them
    let faction-col = accent-red
    let honest-col = accent-green
    // Attacker cluster (tight group)
    for i in range(6) {
      let x = 9.0 + calc.rem(i, 3) * 0.7
      let y = 5.2 - calc.floor(i / 3) * 0.7
      circle((x, y), radius: 0.35, fill: bg-red, stroke: (paint: faction-col, thickness: 0.8pt))
      content((x, y), text(size: 5.5pt, fill: faction-col)[S#str(i + 1)])
    }
    // Faction boundary
    rect((8.5, 4.0), (11.3, 5.8), stroke: (paint: faction-col, thickness: 0.6pt, dash: "dashed"), radius: 5pt)
    content((9.9, 5.95), text(size: 6.5pt, fill: faction-col)[Single faction ($w_i$ cluster)])

    // Honest peers (spread out)
    let honest-pos = ((8.5, 3.0), (10.5, 2.5), (12.5, 3.2), (11.5, 1.8))
    let honest-labels = ("H1", "H2", "H3", "H4")
    for i in range(4) {
      let (x, y) = honest-pos.at(i)
      circle((x, y), radius: 0.35, fill: bg-green, stroke: (paint: honest-col, thickness: 0.8pt))
      content((x, y), text(size: 5.5pt, fill: honest-col)[#honest-labels.at(i)])
    }

    content((11.5, 1.1), text(size: 7pt, style: "italic", fill: honest-col)[MF: 1 faction, intercept low])
  }),
  caption: [IP-based identity is defeated by address proliferation. Rating-pattern identity clusters Sybils into a single faction regardless of IP diversity, because polarity factors reflect behavioral correlation, not network topology.],
) <fig-ip-vs-rating>

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

== Decentralization of Trust Computation

X's Community Notes @communitynotes2023 runs the identical matrix factorization algorithm on centralized infrastructure. X collects all ratings, runs MF on its servers, and publishes the results. X can additionally detect Sybil accounts through IP fingerprinting, browser telemetry, and manual intervention. The natural question is: what replaces X's backstop in a decentralized network?

The answer is that the MF algorithm's Sybil resistance does not depend on centralization. X uses fingerprinting as a supplementary defense, but the core Sybil detection, the polarity factor decomposition, operates entirely on rating patterns. The same mathematics works identically whether computed by a central server or by individual peers. What changes in a decentralized setting is _who runs the computation_, not _how the computation works_.

Shurli addresses trust computation through progressive decentralization in three layers:

#figure(
  cetz.canvas(length: 1cm, {
    import cetz.draw: *

    let box-w = 3.8
    let box-h = 1.8
    let gap-x = 1.2

    // Layer A
    let ax = 0.0
    rect((ax, 0), (ax + box-w, box-h), stroke: (paint: accent-amber, thickness: 0.9pt), fill: bg-warm, radius: 3pt)
    content((ax + box-w/2, box-h - 0.35), text(size: 8pt, weight: "bold", fill: accent-amber)[Layer A: Relay Computes])
    content((ax + box-w/2, box-h - 0.75), text(size: 6.5pt, fill: shurli-dark)[Relay collects signed ratings,])
    content((ax + box-w/2, box-h - 1.05), text(size: 6.5pt, fill: shurli-dark)[runs MF, publishes scores])
    content((ax + box-w/2, box-h - 1.4), text(size: 6.5pt, style: "italic", fill: subtle-gray)[Trust relay, but verifiable])

    // Arrow A→B
    line((ax + box-w + 0.1, box-h/2), (ax + box-w + gap-x - 0.1, box-h/2),
      stroke: (paint: luma(150), thickness: 0.7pt), mark: (end: ">", size: 0.2))

    // Layer B
    let bx = ax + box-w + gap-x
    rect((bx, 0), (bx + box-w, box-h), stroke: (paint: shurli-blue, thickness: 0.9pt), fill: bg-blue, radius: 3pt)
    content((bx + box-w/2, box-h - 0.35), text(size: 8pt, weight: "bold", fill: shurli-blue)[Layer B: Local Verify])
    content((bx + box-w/2, box-h - 0.75), text(size: 6.5pt, fill: shurli-dark)[Peers re-run MF locally,])
    content((bx + box-w/2, box-h - 1.05), text(size: 6.5pt, fill: shurli-dark)[cross-check relay scores])
    content((bx + box-w/2, box-h - 1.4), text(size: 6.5pt, style: "italic", fill: subtle-gray)[Verify relay])

    // Arrow B→C
    line((bx + box-w + 0.1, box-h/2), (bx + box-w + gap-x - 0.1, box-h/2),
      stroke: (paint: luma(150), thickness: 0.7pt), mark: (end: ">", size: 0.2))

    // Layer C
    let cx = bx + box-w + gap-x
    rect((cx, 0), (cx + box-w, box-h), stroke: (paint: accent-green, thickness: 0.9pt), fill: bg-green, radius: 3pt)
    content((cx + box-w/2, box-h - 0.35), text(size: 8pt, weight: "bold", fill: accent-green)[Layer C: Gossip])
    content((cx + box-w/2, box-h - 0.75), text(size: 6.5pt, fill: shurli-dark)[Peers gossip ratings directly,])
    content((cx + box-w/2, box-h - 1.05), text(size: 6.5pt, fill: shurli-dark)[build own matrix, no relay])
    content((cx + box-w/2, box-h - 1.4), text(size: 6.5pt, style: "italic", fill: subtle-gray)[Don't need relay])

    // Bottom annotation
    content((cx/2 + box-w/2, -0.7),
      text(size: 7.5pt, style: "italic", fill: subtle-gray)[
        Each layer reduces trust in the previous one. A alone = trust relay. A+B = verify relay. A+B+C = sovereign.
      ])
  }),
  caption: [Progressive decentralization of trust computation. The mathematical mechanism (MF) is identical at every layer; only the trust assumption changes.],
) <fig-progressive-decentralization>

*Layer A* is semi-centralized: the relay collects signed ratings, runs MF periodically, and publishes both the scores and the input rating matrix. Any peer can download the matrix, re-run MF with the same parameters, and verify that the published scores match within a small tolerance $epsilon$. A relay that fabricates scores must also fabricate a plausible rating matrix that produces those scores under MF, which is computationally harder than running MF honestly. MF reproducibility in practice requires a fixed initialization seed, deterministic iteration order, and single-threaded execution; even then, cross-platform floating-point differences and non-associative summation in parallel reductions introduce last-bit variation @wojcik2022birdwatch. Verification therefore compares intercepts under a convergence tolerance rather than requiring exact equality, the same approach Community Notes uses for its own audit pipeline.

*Layer B* adds local verification. When multiple relays exist, each runs MF independently and publishes results. Peers compare scores across relays and flag divergence. Peers also run local MF on their own direct observations as a sanity check. A relay that selectively drops ratings before publishing the matrix is caught by peers whose local observations disagree with the published matrix.

*Layer C* eliminates the relay from trust computation entirely. Peers gossip interaction ratings directly to neighbors via pubsub. Each peer accumulates a partial view of the global rating matrix through epidemic propagation and runs MF locally. The relay becomes one participant among many, with no special authority over reputation. Layer C produces local trust views, not global consensus: two peers operating on different partial matrices may compute slightly different scores for the same target peer, and this is correct behavior for a decentralized system rather than a weakness. Every trust decision in Shurli is made by a specific observer for a specific purpose (routing, peering selection, access control, transfer acceptance), and every such decision has a specific observer whose local view is the relevant input. The network never queries "the global consensus score of peer $X$" as an atomic value, because no such operation exists in the protocol. Asymmetric local views match how existing decentralized systems (BGP routing, DNS resolution, distributed version control) operate in practice. Asymptotic convergence across peers happens naturally through gossip as the rating graph becomes denser, but is not a correctness requirement for any individual decision.

The five-layer Byzantine defense operates independently of which computation layer is active: (1) bilateral transfer receipts require both peers' signatures to validate a rating, (2) MF itself detects factional coordination, (3) relay observability cross-checks circuit metadata for relayed connections, (4) quasi-clique detection flags coordinated rating groups, and (5) rate limiting ensures each rating requires real network interaction. An attacker must defeat all five layers simultaneously, and critically, the core defense (layer 2: MF faction detection) requires no central operator at all.

== Economic Basis of Sybil Resistance

Matrix factorization detects naive coordinated rating, but pure algorithmic cleverness does not defeat a determined attacker who adapts to the known scoring rules. The entire network is itself, in a sense, a "swarm" that accumulated influence over time. The algorithm alone is not the defense. The defense is what the algorithm, combined with real-world participation cost, makes the attacker actually do.

In proof-of-work systems, the cost is computational work whose output (mined tokens) is tradable. In centralized systems, the cost is identity verification (SMS, government ID, KYC) whose integrity depends on external authorities. Shurli uses neither. The cost in DSP is _real participation over time_: a peer must age at least 7 days before its ratings count toward others' scores, complete a minimum number of genuine interactions, operate under a probationary score cap for 30 days, and produce bilateral transfer receipts signed by counterparties for every interaction that feeds reputation.

The critical property of these costs is that honest users pay them as a byproduct of normal use. A participant who joined to share files, reach their own machines, or run an AI agent is already completing transfers, accumulating interactions, and aging their peer identity. They pay zero marginal cost for reputation accrual. An attacker faces the same requirements, but with an additional algorithmic constraint from Section 3.3: cross-factional agreement is the only path to high intercepts, and cross-factional agreement can only be obtained by genuinely providing value to peers outside the attacker's control. Faction-internal rating produces zero intercept regardless of how many fake nodes participate.

After the probationary period, an attacker who maintained fake nodes through real interactions, earned ratings from peers outside their cluster, and built cross-factional reputation has functionally become an honest participant, because the requirements for attack and honest participation converge. The defense is not "make attacks computationally expensive" but "make attacks indistinguishable from honest participation". This is sufficient for networking decisions (routing, peering, access control) where the cost of a wrong outcome is small and reversible. It is explicitly not sufficient for financial settlement, which requires thermodynamic irreversibility.

The convergence argument handles the _building_ phase of an attack, not the _exit_ phase. A node that participated honestly for six months, accumulated real cross-factional reputation, and then used that trust for a single high-value malicious action is not prevented by the mechanisms described above. Asymmetric rating weights (one data-corruption event costs 75 successful-transfer equivalents) and time decay cause reputation to collapse rapidly after the betrayal, but the betrayal itself is not blocked in the moment it occurs. This is an explicit scope choice: Shurli is designed for interactions where the per-event blast radius is bounded (a failed transfer, a bad routing decision, a dropped relay circuit), so that reputation degradation after the fact is a sufficient response. Interactions where a single defection has catastrophic consequences, such as irreversible financial settlement or legal commitment, require thermodynamic or contractual finality outside DSP's scope and should not be built on reputation alone.

A third defense layers on top of behavioral cost: tiered identity trust via the Shurli naming standard. Peers may optionally bind their identity to verifiable external presence, such as social profiles, messaging accounts, enterprise directory entries, DIDs @w3cdid2026, or domain names. These bindings feed a separate identity trust dimension that combines with behavioral reputation to produce the final score. An honest user links identities they already control at zero cost. An attacker running 10,000 Sybil nodes must forge 10,000 plausible external identities, each with its own history, cross-platform presence, and verifiability. The marginal cost per Sybil grows with the number of identity tiers the target network weighs. Identity trust is optional and pseudonymous participation remains fully supported, but networks that require stronger trust decisions (enterprise agent-to-agent coordination, paid inference markets) can weight identity trust heavily, shifting the attack cost from "operate nodes honestly for 30 days" to "operate nodes honestly for 30 days _and_ maintain real external identities that pass cross-platform verification."

Generative models are making plausible synthetic profiles progressively cheaper, which erodes the per-identity forgery cost over time. This is an arms race the identity-binding defense cannot win on its own. The design response is that identity trust is weighted, not binary: networks set the weight based on their threat model, deployments requiring high assurance can require multiple independent tiers (social plus enterprise plus government-issued), and crucially, behavioral defense (MF faction detection, bilateral receipts, cross-factional rating requirements) remains the primary layer and does not depend on identity at all. A synthetic profile cannot shortcut the requirement to complete real interactions with peers the attacker does not control. The paper's position is that identity trust is one asymmetric cost factor among several, degrading gracefully as synthetic identity generation improves, rather than a linchpin defense.

In authorized private networks, a fourth and strongest defense applies: invitation chains. Every peer traces membership through a chain of prior invitations rooted in trusted parties. Sybil creation requires either compromising an existing peer's identity or convincing a trusted peer to issue an invitation, neither of which has a computational shortcut. Private-mode networks inherit the social trust of their operators at zero ongoing cost to users.

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
    [Finality], [Probabilistic (energy)], [Contractual], [Probabilistic (behavioral)],
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

*Layer 3: Identity and Naming.* A BIP32-style @bip32 hierarchical key tree derives all keys through HKDF-SHA256 with domain separation. The root seed may be generated from a BIP39 mnemonic, SLIP39 shares, or any method that produces sufficient entropy. One backup of the root seed recovers everything. The naming standard @zooko2001names @rfc9498gns provides a five-layer resolution pipeline: PeerID (cryptographic ground truth), DID (standards interop via W3C `did:peer` @w3cdid2026), petname (local, user-assigned), nickname (self-chosen, advisory), and external (resolved via plugins for ENS, DNS, VerusID, or any naming system). The relay is explicitly prevented from becoming a name authority.

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

*Probabilistic trust, not thermodynamic finality.* Both blockchain and DSP provide probabilistic guarantees, but backed by different resources. In proof-of-work, the cost of reversing a transaction grows with accumulated computational work: older transactions become exponentially harder to undo, and time strengthens finality. In DSP, the cost of manipulating a reputation grows with observer diversity and cross-factional agreement, but time works in the opposite direction: older observations decay in relevance, and recent behavior carries more weight. This is by design. A relay that was reliable for two years but started dropping connections last week should lose reputation now, not coast on historical performance. For use cases that require immutable, time-strengthening finality (financial settlement, legal records), blockchain remains appropriate. DSP provides adaptive probabilistic trust sufficient for networking decisions but not for irreversible transactions.

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

