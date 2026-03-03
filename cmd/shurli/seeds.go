package main

// HardcodedSeeds are ultimate-fallback bootstrap peers used when config,
// DNS seeds, and relay addresses are all unavailable.
// These are multiaddrs of known long-running shurli nodes.
//
// Populated after seed node VPS setup. Use multiaddrs, not raw IPs.
var HardcodedSeeds = []string{
	// AU relay (Sydney)
	"/ip4/192.53.169.150/tcp/7777/p2p/12D3KooWJzG2AHRZjVvbyRWmxhpqJJ8LVoGpb3QXv2UXQLpQBB2o",
	"/ip6/2400:8907::2000:18ff:fe80:49b8/tcp/7777/p2p/12D3KooWJzG2AHRZjVvbyRWmxhpqJJ8LVoGpb3QXv2UXQLpQBB2o",
	// SG relay (Singapore)
	"/ip4/139.162.36.65/tcp/7777/p2p/12D3KooWP7KvT3nvucrW44CPkndYU2Xry785LPdV1Xc4WywdX15z",
	"/ip6/2400:8901::2000:55ff:fe6b:2b65/tcp/7777/p2p/12D3KooWP7KvT3nvucrW44CPkndYU2Xry785LPdV1Xc4WywdX15z",
}

// DNSSeedDomain is the domain used for DNS-based bootstrap discovery.
// Shurli queries _dnsaddr.<domain> TXT records for peer multiaddrs,
// following the dnsaddr convention used by IPFS bootstrap.
const DNSSeedDomain = "seeds.shurli.io"
