package filetransfer

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var (
	zstdEncPool sync.Pool
	zstdDecPool sync.Pool
)

func getEncoder() *zstd.Encoder {
	if v := zstdEncPool.Get(); v != nil {
		return v.(*zstd.Encoder)
	}
	// Window size 1MB: sufficient for 256KB-2MB chunks. Default (8MB) caused
	// 2.6 GB memory bloat on the sender — each pooled encoder retained an 8MB
	// history buffer, and sync.Pool kept hundreds alive between GC cycles.
	// BUG-MP-8: OOM-killed the daemon mid-transfer (562MB file).
	//
	// Coupling (ChunkTarget): the window deliberately holds 1 MB even though
	// FT-Y #14's top tier produces 4 MB chunks. Each chunk is compressed as a
	// standalone zstd frame via EncodeAll(), so window < chunk just means the
	// frame references 1 MB of internal history instead of the full 4 MB.
	// Compression ratio drops marginally for the 4 MB tier but resident memory
	// per pooled encoder stays bounded. If a future tier exceeds 4 MB, revisit
	// this trade-off rather than bumping the window proportionally.
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithWindowSize(1<<20), // 1 MB window (was 8 MB default)
	)
	if err != nil {
		panic("zstd encoder init: " + err.Error())
	}
	return enc
}

func putEncoder(enc *zstd.Encoder) { zstdEncPool.Put(enc) }

func getDecoder() *zstd.Decoder {
	if v := zstdDecPool.Get(); v != nil {
		return v.(*zstd.Decoder)
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxMemory(64<<20), // 64 MB max (largest chunk is 2 MB)
		zstd.WithDecoderMaxWindow(32<<20), // 32 MB max window
	)
	if err != nil {
		panic("zstd decoder init: " + err.Error())
	}
	return dec
}

func putDecoder(dec *zstd.Decoder) { zstdDecPool.Put(dec) }

// compressChunk compresses data with zstd. Returns original data if compression
// doesn't save at least 5% (incompressible detection). The bool indicates
// whether the returned data is compressed.
func compressChunk(data []byte) ([]byte, bool) {
	enc := getEncoder()
	compressed := enc.EncodeAll(data, make([]byte, 0, len(data)))
	putEncoder(enc)

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
	dec := getDecoder()
	result, err := dec.DecodeAll(data, make([]byte, 0, maxOutput))
	putDecoder(dec)
	if err != nil {
		return nil, err
	}
	if len(result) > maxOutput {
		return nil, fmt.Errorf("decompressed size %d exceeds limit %d", len(result), maxOutput)
	}
	return result, nil
}
