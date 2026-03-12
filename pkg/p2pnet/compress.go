package p2pnet

import (
	"github.com/klauspost/compress/zstd"
)

// compressChunk compresses data with zstd. Returns original data if compression
// doesn't save at least 5% (incompressible detection). The bool indicates
// whether the returned data is compressed.
func compressChunk(data []byte) ([]byte, bool) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return data, false
	}
	defer enc.Close()

	compressed := enc.EncodeAll(data, make([]byte, 0, len(data)))

	// Skip if ratio < 95% (saves less than 5%).
	if len(compressed) >= len(data)*95/100 {
		return data, false
	}

	return compressed, true
}

// decompressChunk decompresses zstd data with a hard output size limit
// to prevent compression bombs. maxOutput is the maximum allowed decompressed
// size; if exceeded, decompression fails.
func decompressChunk(data []byte, maxOutput int) ([]byte, error) {
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxMemory(uint64(maxOutput)),
		zstd.WithDecoderMaxWindow(32<<20), // 32 MB max window
	)
	if err != nil {
		return nil, err
	}
	defer dec.Close()

	return dec.DecodeAll(data, make([]byte, 0, len(data)*2))
}
