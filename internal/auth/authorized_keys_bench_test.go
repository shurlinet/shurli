package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkLoadAuthorizedKeys5(b *testing.B) {
	dir := b.TempDir()
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, genPeerIDStr(b)+"  # peer-"+string(rune('a'+i)))
	}
	path := filepath.Join(dir, "authorized_keys")
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LoadAuthorizedKeys(path)
	}
}

func BenchmarkLoadAuthorizedKeys50(b *testing.B) {
	dir := b.TempDir()
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, genPeerIDStr(b))
	}
	path := filepath.Join(dir, "authorized_keys")
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LoadAuthorizedKeys(path)
	}
}
