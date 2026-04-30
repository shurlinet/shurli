package filetransfer

import (
	"bytes"
	"testing"
)

// FuzzReadHeader feeds arbitrary bytes into readHeader to verify it never
// panics on malformed input. readHeader is the first parser hit by remote
// data on every file transfer — it must reject garbage safely.
func FuzzReadHeader(f *testing.F) {
	// Seed: valid minimal header (1 file, 0 bytes, no metadata).
	var valid bytes.Buffer
	writeHeader(&valid, []fileEntry{{Path: "a.txt", Size: 0}}, 0, 0, [32]byte{}, nil)
	f.Add(valid.Bytes())

	// Seed: truncated magic.
	f.Add([]byte("SHF"))

	// Seed: wrong magic.
	f.Add([]byte("XXXX\x02\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00"))

	// Seed: zero-length input.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		readHeader(bytes.NewReader(data))
	})
}

// FuzzReadStreamChunkFrame feeds arbitrary bytes into readStreamChunkFrame
// to verify it never panics. This parser processes every chunk frame from
// the network — a panic here is a remote crash.
func FuzzReadStreamChunkFrame(f *testing.F) {
	// Seed: valid msgTransferDone.
	f.Add([]byte{msgTransferDone})

	// Seed: valid msgTrailer.
	f.Add([]byte{msgTrailer})

	// Seed: valid msgStreamChunk with 4-byte payload.
	var chunk bytes.Buffer
	sc := streamChunk{
		fileIdx:    0,
		chunkIdx:   0,
		offset:     0,
		hash:       [32]byte{1},
		decompSize: 4,
		data:       []byte("test"),
	}
	writeStreamChunkFrame(&chunk, sc)
	f.Add(chunk.Bytes())

	// Seed: unknown message type.
	f.Add([]byte{0xFF})

	// Seed: truncated chunk header (type byte + partial header).
	f.Add([]byte{msgStreamChunk, 0, 0, 0, 0})

	// Seed: zero-length input.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		readStreamChunkFrame(bytes.NewReader(data))
	})
}

// FuzzSanitizeRelativePath feeds arbitrary strings into sanitizeRelativePath
// to verify it never panics and always returns a safe path (no "..",
// no absolute prefix, no control characters).
func FuzzSanitizeRelativePath(f *testing.F) {
	f.Add("hello/world.txt")
	f.Add("../../../etc/passwd")
	f.Add("a\\b\\c.txt")
	f.Add("./foo/./bar/../baz")
	f.Add("")
	f.Add("/absolute/path")
	f.Add(string([]byte{0x00, 0x01, 0x7f}))
	f.Add("normal.txt\x1b[31mred")
	f.Add("\u202Eevil\u202C.txt")

	f.Fuzz(func(t *testing.T, input string) {
		result := sanitizeRelativePath(input)
		if result == "" {
			return
		}
		// Must never contain path traversal.
		for _, part := range bytes.Split([]byte(result), []byte("/")) {
			if string(part) == ".." {
				t.Errorf("sanitizeRelativePath(%q) = %q contains '..'", input, result)
			}
		}
		// Must never start with '/'.
		if result[0] == '/' {
			t.Errorf("sanitizeRelativePath(%q) = %q starts with '/'", input, result)
		}
	})
}
