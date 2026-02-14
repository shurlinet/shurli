package config

import (
	"testing"
)

func BenchmarkLoadNodeConfig(b *testing.B) {
	dir := b.TempDir()
	path := writeTestConfig(b, dir, testConfigYAML)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LoadNodeConfig(path)
	}
}

func BenchmarkValidateNodeConfig(b *testing.B) {
	cfg := &NodeConfig{
		Identity:  IdentityConfig{KeyFile: "key"},
		Network:   NetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/0"}},
		Relay:     RelayConfig{Addresses: []string{"/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An"}},
		Discovery: DiscoveryConfig{Rendezvous: "test"},
		Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "/pingpong/1.0.0"}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidateNodeConfig(cfg)
	}
}
