package main

// HardcodedSeeds are ultimate-fallback bootstrap peers used when config,
// DNS seeds, and relay addresses are all unavailable.
// These are multiaddrs of known long-running shurli nodes.
//
// Populated after seed node VPS setup. Use multiaddrs, not raw IPs.
var HardcodedSeeds = []string{
	// Seed nodes will be added here after VPS setup completes.
	// Format: "/ip4/<ip>/tcp/7777/p2p/<peer-id>"
}

// DNSSeedDomain is the domain used for DNS-based bootstrap discovery.
// Shurli queries _dnsaddr.<domain> TXT records for peer multiaddrs,
// following the dnsaddr convention used by IPFS bootstrap.
const DNSSeedDomain = "seeds.shurli.io"
