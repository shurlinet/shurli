package invite

import (
	"testing"
)

func BenchmarkEncode(b *testing.B) {
	token, err := GenerateToken()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Encode(token)
	}
}

func BenchmarkDecode(b *testing.B) {
	token, err := GenerateToken()
	if err != nil {
		b.Fatal(err)
	}
	code, err := Encode(token)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decode(code)
	}
}
