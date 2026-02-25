// Package qr implements a QR Code encoder for terminal display.
//
// Derived from github.com/skip2/go-qrcode (MIT License).
// Copyright (c) 2014 Tom Harwood. See THIRD_PARTY_NOTICES in the repo root.
//
// Original: https://github.com/skip2/go-qrcode
// Modifications: removed PNG/image support (not needed for terminal display),
// flattened sub-packages into single internal package, exported only the
// minimal API needed by Shurli.
package qr

import (
	"bytes"
	"errors"
	"log"
)

// QRCode represents a valid encoded QR Code.
type QRCode struct {
	Content       string
	Level         RecoveryLevel
	VersionNumber int
	DisableBorder bool

	encoder *dataEncoder
	version qrCodeVersion
	data    *bitset
	symbol  *symbol
	mask    int
}

// New constructs a QRCode.
//
//	qr, err := qr.New("my content", qr.Medium)
//
// An error occurs if the content is too long.
func New(content string, level RecoveryLevel) (*QRCode, error) {
	encoders := []dataEncoderType{
		dataEncoderType1To9,
		dataEncoderType10To26,
		dataEncoderType27To40,
	}

	var encoder *dataEncoder
	var encoded *bitset
	var chosenVersion *qrCodeVersion
	var err error

	for _, t := range encoders {
		encoder = newDataEncoder(t)
		encoded, err = encoder.encode([]byte(content))
		if err != nil {
			continue
		}
		chosenVersion = chooseQRCodeVersion(level, encoder, encoded.len())
		if chosenVersion != nil {
			break
		}
	}

	if err != nil {
		return nil, err
	} else if chosenVersion == nil {
		return nil, errors.New("content too long to encode")
	}

	return &QRCode{
		Content:       content,
		Level:         level,
		VersionNumber: chosenVersion.version,
		encoder:       encoder,
		data:          encoded,
		version:       *chosenVersion,
	}, nil
}

// Bitmap returns the QR Code as a 2D array of 1-bit pixels.
// bitmap[y][x] is true if the pixel at (x, y) is set.
// Includes the required quiet zone border.
func (q *QRCode) Bitmap() [][]bool {
	q.encode()
	return q.symbol.bitmap()
}

// ToSmallString produces a compact multi-line string using Unicode half-block
// characters. The output is half the height of a full-block rendering,
// suitable for terminal display.
func (q *QRCode) ToSmallString(inverseColor bool) string {
	bits := q.Bitmap()
	var buf bytes.Buffer
	for y := 0; y < len(bits)-1; y += 2 {
		for x := range bits[y] {
			if bits[y][x] == bits[y+1][x] {
				if bits[y][x] != inverseColor {
					buf.WriteString(" ")
				} else {
					buf.WriteString("█")
				}
			} else {
				if bits[y][x] != inverseColor {
					buf.WriteString("▄")
				} else {
					buf.WriteString("▀")
				}
			}
		}
		buf.WriteString("\n")
	}
	if len(bits)%2 == 1 {
		y := len(bits) - 1
		for x := range bits[y] {
			if bits[y][x] != inverseColor {
				buf.WriteString(" ")
			} else {
				buf.WriteString("▀")
			}
		}
		buf.WriteString("\n")
	}
	return buf.String()
}

// encode completes the QR Code encoding: terminator bits, padding,
// error correction, and mask selection.
func (q *QRCode) encode() {
	numTerminatorBits := q.version.numTerminatorBitsRequired(q.data.len())
	q.addTerminatorBits(numTerminatorBits)
	q.addPadding()
	encoded := q.encodeBlocks()

	const numMasks = 8
	penalty := 0

	for mask := 0; mask < numMasks; mask++ {
		s, err := buildRegularSymbol(q.version, mask, encoded, !q.DisableBorder)
		if err != nil {
			log.Panic(err.Error())
		}
		if numEmpty := s.numEmptyModules(); numEmpty != 0 {
			log.Panicf("bug: numEmptyModules is %d (expected 0) (version=%d)",
				numEmpty, q.VersionNumber)
		}
		p := s.penaltyScore()
		if q.symbol == nil || p < penalty {
			q.symbol = s
			q.mask = mask
			penalty = p
		}
	}
}

func (q *QRCode) addTerminatorBits(numTerminatorBits int) {
	q.data.appendNumBools(numTerminatorBits, false)
}

func (q *QRCode) encodeBlocks() *bitset {
	type dataBlock struct {
		data          *bitset
		ecStartOffset int
	}

	block := make([]dataBlock, q.version.numBlocks())
	start, end, blockID := 0, 0, 0

	for _, b := range q.version.block {
		for j := 0; j < b.numBlocks; j++ {
			start = end
			end = start + b.numDataCodewords*8
			numErrorCodewords := b.numCodewords - b.numDataCodewords
			block[blockID].data = rsEncode(q.data.substr(start, end), numErrorCodewords)
			block[blockID].ecStartOffset = end - start
			blockID++
		}
	}

	result := newBitset()

	working := true
	for i := 0; working; i += 8 {
		working = false
		for j, b := range block {
			if i >= block[j].ecStartOffset {
				continue
			}
			result.append(b.data.substr(i, i+8))
			working = true
		}
	}

	working = true
	for i := 0; working; i += 8 {
		working = false
		for j, b := range block {
			offset := i + block[j].ecStartOffset
			if offset >= block[j].data.len() {
				continue
			}
			result.append(b.data.substr(offset, offset+8))
			working = true
		}
	}

	result.appendNumBools(q.version.numRemainderBits, false)
	return result
}

func (q *QRCode) addPadding() {
	numDataBits := q.version.numDataBits()
	if q.data.len() == numDataBits {
		return
	}
	q.data.appendNumBools(q.version.numBitsToPadToCodeword(q.data.len()), false)

	padding := [2]*bitset{
		newBitset(true, true, true, false, true, true, false, false),
		newBitset(false, false, false, true, false, false, false, true),
	}

	i := 0
	for numDataBits-q.data.len() >= 8 {
		q.data.append(padding[i])
		i = 1 - i
	}

	if q.data.len() != numDataBits {
		log.Panicf("BUG: got len %d, expected %d", q.data.len(), numDataBits)
	}
}
