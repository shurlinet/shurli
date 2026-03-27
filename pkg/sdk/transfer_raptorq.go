package sdk

import (
	"fmt"

	"github.com/xssnick/raptorq"
)

// RaptorQ fountain code wrapper for multi-source file transfer.
//
// RaptorQ generates repair symbols from source data. A receiver needs only K
// symbols (source or repair, any combination) out of N to reconstruct the
// original data. This makes it ideal for multi-peer download: different peers
// can send different symbol ranges and the receiver doesn't care about order
// or overlap.

// raptorqSymbolSize is the symbol size for RaptorQ encoding.
// Must match across encoder and decoder. 1024 bytes is a good balance
// between overhead and repair granularity.
const raptorqSymbolSize = 1024

// raptorqRepairRatio is the ratio of repair symbols to source symbols.
// 0.2 = 20% overhead (K source symbols + 0.2*K repair symbols).
const raptorqRepairRatio = 0.2

// raptorqEncoder wraps a RaptorQ encoder for a single chunk or block.
type raptorqEncoder struct {
	enc     *raptorq.Encoder
	codec   *raptorq.RaptorQ
	dataLen int
	k       uint32 // number of source symbols
}

// newRaptorQEncoder creates a fountain encoder for the given data block.
func newRaptorQEncoder(data []byte) (*raptorqEncoder, error) {
	codec := raptorq.NewRaptorQ(raptorqSymbolSize)
	enc, err := codec.CreateEncoder(data)
	if err != nil {
		return nil, fmt.Errorf("raptorq encode: %w", err)
	}
	return &raptorqEncoder{
		enc:     enc,
		codec:   codec,
		dataLen: len(data),
		k:       enc.BaseSymbolsNum(),
	}, nil
}

// sourceSymbolCount returns the number of source symbols (K).
func (e *raptorqEncoder) sourceSymbolCount() uint32 {
	return e.k
}

// repairSymbolCount returns the recommended number of repair symbols.
func (e *raptorqEncoder) repairSymbolCount() int {
	n := int(float64(e.k) * raptorqRepairRatio)
	if n < 1 {
		n = 1
	}
	return n
}

// genSymbol generates a symbol by ID. IDs 0..K-1 are source symbols;
// IDs >= K are repair symbols (fountain: unlimited supply).
func (e *raptorqEncoder) genSymbol(id uint32) []byte {
	return e.enc.GenSymbol(id)
}

// raptorqDecoder wraps a RaptorQ decoder for a single chunk or block.
type raptorqDecoder struct {
	dec     *raptorq.Decoder
	codec   *raptorq.RaptorQ
	dataLen uint32
}

// newRaptorQDecoder creates a fountain decoder expecting data of the given size.
func newRaptorQDecoder(dataSize uint32) (*raptorqDecoder, error) {
	codec := raptorq.NewRaptorQ(raptorqSymbolSize)
	dec, err := codec.CreateDecoder(dataSize)
	if err != nil {
		return nil, fmt.Errorf("raptorq decoder: %w", err)
	}
	return &raptorqDecoder{
		dec:     dec,
		codec:   codec,
		dataLen: dataSize,
	}, nil
}

// addSymbol adds a received symbol. Returns true when enough symbols have
// been collected to attempt decoding.
func (d *raptorqDecoder) addSymbol(id uint32, data []byte) (bool, error) {
	return d.dec.AddSymbol(id, data)
}

// decode reconstructs the original data from collected symbols.
// Returns (ok, data, err). ok=false means not enough symbols yet.
func (d *raptorqDecoder) decode() (bool, []byte, error) {
	return d.dec.Decode()
}

// requiredSymbols returns K, the minimum number of symbols needed to decode.
func (d *raptorqDecoder) requiredSymbols() uint32 {
	return d.dec.FastSymbolsNumRequired()
}
