package p2pnet

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// streamOpener opens a new stream to the same peer for parallel transfer.
type streamOpener func() (network.Stream, error)

// Parallel stream constants.
const (
	// Adaptive stream defaults by transport type.
	parallelStreamsLAN       = 8
	parallelStreamsLANMax    = 32
	parallelStreamsDirect   = 4
	parallelStreamsDirectMax = 20

	// Minimum chunks per stream to justify parallelism.
	minChunksPerStream = 4
)

// adaptiveStreamCount returns the optimal stream count based on transport type and chunk count.
func adaptiveStreamCount(transport TransportType, chunkCount, requested int) int {
	if requested > 0 {
		return clampStreams(requested, transport)
	}

	var defaultN int
	switch transport {
	case TransportLAN:
		defaultN = parallelStreamsLAN
	case TransportDirect:
		defaultN = parallelStreamsDirect
	default:
		return 1 // relay or unknown: single stream
	}

	// Don't use more streams than makes sense for the chunk count.
	maxByChunks := chunkCount / minChunksPerStream
	if maxByChunks < 1 {
		maxByChunks = 1
	}
	if defaultN > maxByChunks {
		defaultN = maxByChunks
	}

	return defaultN
}

// clampStreams ensures the stream count is within transport-specific bounds.
func clampStreams(n int, transport TransportType) int {
	if n < 1 {
		return 1
	}
	var max int
	switch transport {
	case TransportLAN:
		max = parallelStreamsLANMax
	case TransportDirect:
		max = parallelStreamsDirectMax
	default:
		return 1
	}
	if n > max {
		return max
	}
	return n
}

// --- Parallel send ---

// sendParallel opens N-1 worker streams and distributes chunks across all streams.
// The control stream (w) carries stream 0's chunk range + msgTransferDone.
// Each worker stream sends msgWorkerHello + rootHash, then its chunk frames, then closes.
func (ts *TransferService) sendParallel(
	controlRW io.ReadWriter,
	openStream streamOpener,
	m *transferManifest,
	chunks []chunkEntry,
	parity []parityChunk,
	progress *TransferProgress,
	numStreams int,
) error {
	if numStreams <= 1 {
		// Fallback to sequential.
		return ts.sendChunked(controlRW, m, chunks, parity, progress)
	}

	// Send manifest on control stream and wait for accept/reject.
	if err := writeManifest(controlRW, m); err != nil {
		return fmt.Errorf("send manifest: %w", err)
	}
	resp, err := readMsg(controlRW)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	switch resp {
	case msgReject:
		return fmt.Errorf("peer rejected transfer")
	case msgRejectReason:
		reasonByte, err := readMsg(controlRW)
		if err != nil {
			return fmt.Errorf("peer rejected transfer (could not read reason)")
		}
		return fmt.Errorf("peer rejected transfer: %s", RejectReasonString(reasonByte))
	case msgResumeRequest:
		// Resume not supported in parallel mode; fall back to sequential.
		// Read the bitfield, send ResumeResponse, then send missing chunks single-stream.
		slog.Info("file-transfer: resume requested during parallel send, falling back to sequential")
		bfData, bfErr := readResumePayload(controlRW)
		if bfErr != nil {
			return fmt.Errorf("read resume payload: %w", bfErr)
		}
		have := &bitfield{bits: make([]byte, (m.ChunkCount+7)/8), n: m.ChunkCount}
		copy(have.bits, bfData)
		if wErr := writeMsg(controlRW, msgResumeResponse); wErr != nil {
			return fmt.Errorf("send resume response: %w", wErr)
		}
		progress.setStatus("active")
		var totalSent int64
		sent := 0
		for i, c := range chunks {
			if have.has(i) {
				continue
			}
			if wErr := writeChunkFrame(controlRW, i, c.data); wErr != nil {
				return fmt.Errorf("send chunk %d: %w", i, wErr)
			}
			totalSent += int64(len(c.data))
			sent++
			progress.updateChunks(totalSent, sent+have.count())
			progress.addWireBytes(int64(len(c.data)))
		}
		if pErr := sendParityChunks(controlRW, parity, m.ChunkCount); pErr != nil {
			return pErr
		}
		return writeMsg(controlRW, msgTransferDone)
	case msgAccept:
		// Accepted, continue to parallel send.
	default:
		return fmt.Errorf("unexpected response: 0x%02x", resp)
	}

	progress.setStatus("active")
	progress.initStreams(numStreams)

	// Partition data chunks across streams (round-robin).
	partitions := make([][]int, numStreams)
	for i := range chunks {
		slot := i % numStreams
		partitions[slot] = append(partitions[slot], i)
	}

	// All parity chunks go on stream 0 (control) after data.

	// Track progress atomically across goroutines.
	var totalSent atomic.Int64
	var chunksDone atomic.Int32

	// Open worker streams.
	workers := make([]network.Stream, numStreams-1)
	for i := 0; i < numStreams-1; i++ {
		ws, err := openStream()
		if err != nil {
			// Close already-opened workers.
			for j := 0; j < i; j++ {
				workers[j].Close()
			}
			slog.Warn("file-transfer: parallel stream open failed, falling back to sequential",
				"error", err, "attempted", i+1, "total", numStreams)
			// Fallback to sequential on control stream.
			return ts.sendChunked(controlRW, m, chunks, parity, progress)
		}
		workers[i] = ws
		ws.SetDeadline(time.Now().Add(transferStreamDeadline))
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value

	recordErr := func(err error) {
		if err != nil {
			firstErr.CompareAndSwap(nil, err)
		}
	}

	// Worker goroutines (streams 1..N-1).
	for i, ws := range workers {
		wg.Add(1)
		go func(streamIdx int, s network.Stream, partition []int) {
			defer wg.Done()
			defer s.Close()

			// Send worker hello: msgWorkerHello + rootHash(32).
			if err := writeWorkerHello(s, m.RootHash); err != nil {
				recordErr(fmt.Errorf("worker %d hello: %w", streamIdx, err))
				return
			}

			// Send assigned chunks.
			for _, idx := range partition {
				if err := writeChunkFrame(s, idx, chunks[idx].data); err != nil {
					recordErr(fmt.Errorf("worker %d chunk %d: %w", streamIdx, idx, err))
					return
				}
				wireBytes := int64(len(chunks[idx].data))
				totalSent.Add(wireBytes)
				done := chunksDone.Add(1)
				progress.updateChunks(totalSent.Load(), int(done))
				progress.addWireBytes(wireBytes)
				progress.updateStream(streamIdx, wireBytes)
			}
		}(i+1, ws, partitions[i+1])
	}

	// Stream 0 (control): send its chunk partition.
	for _, idx := range partitions[0] {
		if err := writeChunkFrame(controlRW, idx, chunks[idx].data); err != nil {
			recordErr(fmt.Errorf("control chunk %d: %w", idx, err))
			break
		}
		wireBytes := int64(len(chunks[idx].data))
		totalSent.Add(wireBytes)
		done := chunksDone.Add(1)
		progress.updateChunks(totalSent.Load(), int(done))
		progress.addWireBytes(wireBytes)
		progress.updateStream(0, wireBytes)
	}

	// Wait for all workers to finish.
	wg.Wait()

	if errVal := firstErr.Load(); errVal != nil {
		return errVal.(error)
	}

	// Send parity on control stream.
	if err := sendParityChunks(controlRW, parity, m.ChunkCount); err != nil {
		return err
	}

	// Signal done.
	return writeMsg(controlRW, msgTransferDone)
}

// --- Parallel receive ---

// parallelSession coordinates chunk reception from multiple streams for one transfer.
type parallelSession struct {
	rootHash   [32]byte
	manifest   *transferManifest
	tmpFile    *os.File
	tmpPath    string
	have       *bitfield
	offsets    []int64
	progress   *TransferProgress
	compressed bool
	hasErasure bool

	mu           sync.Mutex
	totalWritten int64
	parityData   map[int][]byte
	corrupted    []int
	nextWorkerID int32 // atomically incremented to assign stream indices to workers

	// done is closed when receiveChunked completes.
	done chan struct{}
	// chunks receives verified chunk data from any stream.
	chunks chan parallelChunk
}

// parallelChunk is a verified chunk delivered from any stream.
type parallelChunk struct {
	index       int
	data        []byte
	isParity    bool
	streamIndex int // which stream delivered this chunk (0-indexed)
}

// writeWorkerHello writes the worker hello message: msgWorkerHello + rootHash.
func writeWorkerHello(w io.Writer, rootHash [32]byte) error {
	var buf [33]byte
	buf[0] = msgWorkerHello
	copy(buf[1:], rootHash[:])
	_, err := w.Write(buf[:])
	return err
}

// readWorkerHello reads the rootHash after msgWorkerHello byte has been consumed.
func readWorkerHello(r io.Reader) ([32]byte, error) {
	var hash [32]byte
	_, err := io.ReadFull(r, hash[:])
	return hash, err
}

// handleWorkerStreamFromReader processes an incoming parallel worker stream.
// The caller has already peeked the first byte (msgWorkerHello) via a bufio.Reader.
// All reads go through r to avoid losing the buffered data.
func (ts *TransferService) handleWorkerStreamFromReader(s network.Stream, r *bufio.Reader) {
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()
	short := remotePeer.String()[:16] + "..."

	// Consume the msgWorkerHello byte (already peeked by caller).
	var helloByte [1]byte
	if _, err := io.ReadFull(r, helloByte[:]); err != nil {
		slog.Debug("file-transfer: worker hello read failed", "peer", short, "error", err)
		return
	}

	rootHash, err := readWorkerHello(r)
	if err != nil {
		slog.Debug("file-transfer: worker rootHash read failed", "peer", short, "error", err)
		return
	}

	// Look up session.
	ts.mu.RLock()
	session, ok := ts.parallelSessions[rootHash]
	ts.mu.RUnlock()

	if !ok {
		slog.Debug("file-transfer: no session for worker stream", "peer", short)
		return
	}

	// Assign a stream index (workers start at 1, control is 0).
	streamIdx := int(atomic.AddInt32(&session.nextWorkerID, 1))

	slog.Debug("file-transfer: worker stream attached", "peer", short, "stream", streamIdx)

	// Read chunks and deliver to session.
	for {
		select {
		case <-session.done:
			return
		default:
		}

		index, wireData, err := readChunkFrame(r)
		if err != nil {
			return // stream closed or error
		}
		if index == -1 {
			return // done signal (shouldn't happen on workers, but handle gracefully)
		}

		session.chunks <- parallelChunk{
			index:       index,
			data:        wireData,
			isParity:    index >= session.manifest.ChunkCount,
			streamIndex: streamIdx,
		}
	}
}

// registerParallelSession registers a session for worker streams to find.
func (ts *TransferService) registerParallelSession(rootHash [32]byte, session *parallelSession) {
	ts.mu.Lock()
	if ts.parallelSessions == nil {
		ts.parallelSessions = make(map[[32]byte]*parallelSession)
	}
	ts.parallelSessions[rootHash] = session
	ts.mu.Unlock()
}

// unregisterParallelSession removes a session.
func (ts *TransferService) unregisterParallelSession(rootHash [32]byte) {
	ts.mu.Lock()
	delete(ts.parallelSessions, rootHash)
	ts.mu.Unlock()
}

// receiveParallel reads chunks from both the control stream and the parallel chunk channel.
// It coordinates writes from multiple sources into a single file.
func (ts *TransferService) receiveParallel(
	controlReader io.Reader,
	session *parallelSession,
	ckpt *transferCheckpoint,
) error {
	m := session.manifest
	offsets := session.offsets
	have := session.have
	compressed := session.compressed
	hasErasure := session.hasErasure
	progress := session.progress

	// Seed progress from checkpoint.
	if have.count() > 0 {
		var seeded int64
		for i := 0; i < m.ChunkCount; i++ {
			if have.has(i) {
				seeded += int64(m.ChunkSizes[i])
			}
		}
		session.mu.Lock()
		session.totalWritten = seeded
		session.mu.Unlock()
		progress.updateChunks(seeded, have.count())
	}

	// Process a single chunk (from any source). streamIdx identifies which stream delivered it.
	processChunk := func(index int, wireData []byte, streamIdx int) error {
		progress.addWireBytes(int64(len(wireData)))

		// Parity chunk.
		if index >= m.ChunkCount && index < m.ChunkCount+m.ParityCount {
			parityIdx := index - m.ChunkCount
			hash := blake3Sum(wireData)
			session.mu.Lock()
			if hash == m.ParityHashes[parityIdx] {
				session.parityData[parityIdx] = wireData
			}
			session.mu.Unlock()
			return nil
		}

		// Validate bounds.
		if index < 0 || index >= m.ChunkCount {
			return fmt.Errorf("chunk index out of range: %d", index)
		}

		session.mu.Lock()
		if have.has(index) {
			session.mu.Unlock()
			return nil // duplicate
		}
		session.mu.Unlock()

		// Decompress if needed.
		chunkData := wireData
		if compressed {
			maxDecomp := len(wireData) * maxDecompressRatio
			if maxDecomp > maxDecompressedChunk {
				maxDecomp = maxDecompressedChunk
			}
			if decompressed, err := decompressChunk(wireData, maxDecomp); err == nil {
				chunkData = decompressed
			}
		}

		// Verify hash.
		hash := blake3Hash(chunkData)
		if hash != m.ChunkHashes[index] {
			if hasErasure {
				session.mu.Lock()
				session.corrupted = append(session.corrupted, index)
				session.mu.Unlock()
				return nil
			}
			return fmt.Errorf("chunk %d hash mismatch: corrupted", index)
		}

		// Verify size.
		if uint32(len(chunkData)) != m.ChunkSizes[index] {
			return fmt.Errorf("chunk %d size mismatch: got %d, expected %d",
				index, len(chunkData), m.ChunkSizes[index])
		}

		// Write at correct offset.
		if _, err := session.tmpFile.WriteAt(chunkData, offsets[index]); err != nil {
			return fmt.Errorf("write chunk %d: %w", index, err)
		}

		session.mu.Lock()
		have.set(index)
		session.totalWritten += int64(len(chunkData))
		tw := session.totalWritten
		haveCount := have.count()
		session.mu.Unlock()

		progress.updateChunks(tw, haveCount)
		progress.updateStream(streamIdx, int64(len(chunkData)))
		return nil
	}

	// Read from control stream + parallel chunk channel concurrently.
	// The control goroutine reads until it gets the done signal (index == -1)
	// or an error. It does not count frames to avoid racing with workers
	// that also update the have bitfield.
	controlDone := make(chan error, 1)
	go func() {
		for {
			index, wireData, err := readChunkFrame(controlReader)
			if err != nil {
				controlDone <- fmt.Errorf("control read: %w", err)
				return
			}
			if index == -1 {
				break // done signal
			}
			if err := processChunk(index, wireData, 0); err != nil {
				controlDone <- err
				return
			}
		}
		controlDone <- nil
	}()

	// Process chunks from worker streams.
	controlFinished := false
	workerDone := false
	for !workerDone {
		select {
		case chunk, ok := <-session.chunks:
			if !ok {
				workerDone = true
				break
			}
			if err := processChunk(chunk.index, chunk.data, chunk.streamIndex); err != nil {
				close(session.done)
				<-controlDone
				return err
			}
			// If control already finished and we have all chunks, stop.
			if controlFinished && have.missing() == 0 {
				workerDone = true
			}
		case err := <-controlDone:
			// Control stream finished (got done signal or error).
			if err != nil {
				close(session.done)
				return err
			}
			controlFinished = true
			// If all chunks received, no need to wait for workers.
			if have.missing() == 0 {
				workerDone = true
			}
			// Otherwise keep draining worker chunks.
		}
	}

	// Wait for control to finish if we exited the worker loop.
	if !controlFinished {
		if err := <-controlDone; err != nil {
			return err
		}
	}

	// Close the session so workers exit.
	close(session.done)

	// Check for missing chunks.
	session.mu.Lock()
	corrupted := session.corrupted
	session.mu.Unlock()

	if len(corrupted) > 0 && hasErasure {
		slog.Info("file-transfer: attempting RS reconstruction",
			"corrupted", len(corrupted), "parity_available", len(session.parityData))
		if err := ts.rsReconstruct(session.tmpFile, m, offsets, corrupted, session.parityData); err != nil {
			return fmt.Errorf("RS reconstruction: %w", err)
		}
		for _, idx := range corrupted {
			have.set(idx)
			session.totalWritten += int64(m.ChunkSizes[idx])
		}
		progress.updateChunks(session.totalWritten, have.count())
	} else if have.missing() > 0 {
		return fmt.Errorf("transfer incomplete: %d chunks missing", have.missing())
	}

	return nil
}

// workerHelloSize is the wire size of a worker hello message.
const workerHelloSize = 1 + 32 // msgWorkerHello + rootHash

// readChunkFrameHeader reads just the 9-byte chunk frame header.
// Returns msgType, index, dataLen.
func readChunkFrameHeader(r io.Reader) (byte, int, int, error) {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, 0, 0, err
	}
	msgType := header[0]
	index := int(binary.BigEndian.Uint32(header[1:5]))
	dataLen := int(binary.BigEndian.Uint32(header[5:9]))
	return msgType, index, dataLen, nil
}
