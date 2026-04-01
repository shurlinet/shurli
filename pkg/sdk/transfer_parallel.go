package sdk

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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

	// workerHelloSize is the wire size of a worker hello message.
	// msgWorkerHello(1) + transferID(32) = 33
	workerHelloSize = 1 + 32
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

// --- Worker hello (I5: transferID instead of rootHash) ---

// writeWorkerHello writes the worker hello message: msgWorkerHello + transferID.
// transferID is used for session routing instead of rootHash (I5).
func writeWorkerHello(w io.Writer, transferID [32]byte) error {
	var buf [33]byte
	buf[0] = msgWorkerHello
	copy(buf[1:], transferID[:])
	_, err := w.Write(buf[:])
	return err
}

// readWorkerHello reads the transferID after msgWorkerHello byte has been consumed.
func readWorkerHello(r io.Reader) ([32]byte, error) {
	var id [32]byte
	_, err := io.ReadFull(r, id[:])
	return id, err
}

// --- Parallel send (streaming protocol) ---

// sendParallel distributes streaming chunks from the producer channel across N worker streams
// and the control stream. It is called from streamingSend when parallel streams are available.
//
// The producer goroutine feeds chunks via ch. This function reads from ch and round-robins
// chunks to worker goroutines. After the producer closes ch, all workers must finish writing
// before the trailer is written on the control stream (R3-IMP2: WaitGroup prevents
// chunk-after-trailer race).
//
// The caller (streamingSend) has already written the header and received accept on controlRW.
// This function handles only the chunk distribution + trailer write.
func (ts *TransferService) sendParallel(
	controlRW io.ReadWriter,
	openStream streamOpener,
	transferID [32]byte,
	ch <-chan streamChunk,
	producerDone <-chan producerResult,
	progress *TransferProgress,
	numStreams int,
) (producerResult, error) {
	var zeroResult producerResult

	progress.initStreams(numStreams)

	// Open worker streams (1..N-1).
	workers := make([]network.Stream, 0, numStreams-1)
	for i := 0; i < numStreams-1; i++ {
		ws, err := openStream()
		if err != nil {
			// Clean up already-opened workers.
			for _, w := range workers {
				w.Close()
			}
			slog.Warn("file-transfer: parallel stream open failed, falling back to single stream",
				"error", err, "attempted", i+1, "total", numStreams)

			// Fallback: send all chunks on control stream only (header already accepted).
			return ts.sendSingleStream(controlRW, ch, producerDone, progress)
		}
		ws.SetDeadline(time.Now().Add(transferStreamDeadline))
		workers = append(workers, ws)
	}

	// D1 fix: compose cancel func to reset workers AND preserve control stream reset.
	progress.mu.Lock()
	prevCancel := progress.cancelFunc
	progress.mu.Unlock()
	progress.setCancelFunc(func() {
		if prevCancel != nil {
			prevCancel()
		}
		for _, ws := range workers {
			ws.Reset()
		}
	})

	// Send worker hello on each worker stream.
	for i, ws := range workers {
		if err := writeWorkerHello(ws, transferID); err != nil {
			// Clean up all workers.
			for _, w := range workers {
				w.Close()
			}
			return zeroResult, fmt.Errorf("worker %d hello: %w", i+1, err)
		}
	}

	// Per-worker channels for chunk distribution.
	workerChs := make([]chan streamChunk, numStreams)
	for i := range workerChs {
		workerChs[i] = make(chan streamChunk, 32) // buffer per worker (reduces goroutine scheduling overhead)
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value
	// errCh signals the distributor to stop when any worker fails.
	errCh := make(chan struct{}, 1)

	recordErr := func(err error) {
		if err != nil {
			if firstErr.CompareAndSwap(nil, err) {
				select {
				case errCh <- struct{}{}:
				default:
				}
			}
		}
	}

	// Track progress atomically across goroutines.
	var totalSent atomic.Int64
	var chunksDone atomic.Int32

	// Worker goroutines (streams 1..N-1): read from their channel, write to wire.
	for i, ws := range workers {
		wg.Add(1)
		go func(streamIdx int, s network.Stream, wch <-chan streamChunk) {
			defer wg.Done()
			defer s.Close()

			for sc := range wch {
				if err := writeStreamChunkFrame(s, sc); err != nil {
					recordErr(fmt.Errorf("worker %d chunk %d: %w", streamIdx, sc.chunkIdx, err))
					return
				}
				wireBytes := int64(len(sc.data))
				totalSent.Add(wireBytes)
				done := chunksDone.Add(1)
				progress.updateChunks(totalSent.Load(), int(done))
				progress.addWireBytes(wireBytes)
				progress.updateStream(streamIdx, wireBytes)
			}
			// Flush buffered writes before closing the stream. Without this,
			// tiny compressed chunks (e.g. 30 bytes) can remain in QUIC's write
			// buffer and get discarded when s.Close() fires. CloseWrite signals
			// the write half is done, forcing QUIC to flush.
			if cw, ok := s.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			}
		}(i+1, ws, workerChs[i+1])
	}

	// Control stream goroutine (stream 0): read from its channel, write to wire.
	wg.Add(1)
	go func() {
		defer wg.Done()

		for sc := range workerChs[0] {
			if err := writeStreamChunkFrame(controlRW, sc); err != nil {
				recordErr(fmt.Errorf("control chunk %d: %w", sc.chunkIdx, err))
				return
			}
			wireBytes := int64(len(sc.data))
			totalSent.Add(wireBytes)
			done := chunksDone.Add(1)
			progress.updateChunks(totalSent.Load(), int(done))
			progress.addWireBytes(wireBytes)
			progress.updateStream(0, wireBytes)
		}
	}()

	// Distribute chunks from producer to per-worker channels (round-robin).
	// If any worker errors, stop distributing to avoid blocking on a dead worker's channel.
	streamIdx := 0
	distribDone := false
	for sc := range ch {
		if distribDone {
			continue // drain producer channel to let it finish
		}
		select {
		case workerChs[streamIdx%numStreams] <- sc:
			streamIdx++
		case <-errCh:
			distribDone = true
			// Continue draining ch to unblock the producer goroutine.
		}
	}

	// Close all per-worker channels to signal workers to finish.
	for _, wch := range workerChs {
		close(wch)
	}

	// R3-IMP2: Wait for ALL workers to finish writing before trailer.
	// This prevents chunk-after-trailer race where a worker stream has in-flight
	// writes when the trailer arrives at the receiver.
	wg.Wait()

	if errVal := firstErr.Load(); errVal != nil {
		return zeroResult, errVal.(error)
	}

	// Wait for producer result (producer channel is already closed).
	result := <-producerDone
	if result.err != nil {
		return zeroResult, fmt.Errorf("chunk producer: %w", result.err)
	}

	return result, nil
}

// sendSingleStream is the fallback when parallel streams can't be opened.
// Sends all chunks from the producer channel on a single stream.
func (ts *TransferService) sendSingleStream(
	rw io.ReadWriter,
	ch <-chan streamChunk,
	producerDone <-chan producerResult,
	progress *TransferProgress,
) (producerResult, error) {
	var zeroResult producerResult
	var totalSent int64
	chunksSent := 0

	for sc := range ch {
		if err := writeStreamChunkFrame(rw, sc); err != nil {
			// Don't block on producerDone here. The caller (streamingSend) has
			// defer cancel() which cancels the producer context. The producer then
			// closes ch (unblocking this range) and sends to done (buffered, cap 1).
			// Blocking here would deadlock: producer blocked on ch <- sc, us on <-done.
			return zeroResult, fmt.Errorf("send chunk %d: %w", sc.chunkIdx, err)
		}
		totalSent += int64(len(sc.data))
		chunksSent++
		progress.updateChunks(totalSent, chunksSent)
		progress.addWireBytes(int64(len(sc.data)))
	}

	result := <-producerDone
	if result.err != nil {
		return zeroResult, fmt.Errorf("chunk producer: %w", result.err)
	}

	return result, nil
}

// --- Parallel receive ---

// parallelSession coordinates chunk reception from multiple streams for one transfer.
// Revised: uses transferID for session routing (I5), stores control peer ID for
// worker verification (C5), uses streamReceiveState for chunk processing.
type parallelSession struct {
	transferID [32]byte      // session key for worker stream routing (I5)
	controlPID peer.ID       // peer ID from control stream for worker verification (C5)
	state      *streamReceiveState
	progress   *TransferProgress

	// Checkpoint state for cross-session resume (R3-IMP5, R4-IMP2, N10).
	contentKey [32]byte // BLAKE3 of sorted paths + sizes
	receiveDir string   // destination directory for checkpoint files
	flags      uint8    // transfer flags (compression, erasure)

	mu           sync.Mutex
	nextWorkerID int32 // atomically incremented to assign stream indices to workers

	// D1 fix: track attached worker streams so cancel can reset them.
	workerStreams []network.Stream

	// done is closed when receiveParallel completes or cancel fires.
	// Worker streams check this to exit their read loops.
	done chan struct{}
	// chunks receives streaming chunk data from worker streams.
	chunks chan streamChunk
}

// resetWorkerStreams resets all attached worker streams to unblock
// any goroutines stuck in readStreamChunkFrame after cancel or completion.
func (s *parallelSession) resetWorkerStreams() {
	s.mu.Lock()
	streams := s.workerStreams
	s.workerStreams = nil
	s.mu.Unlock()
	for _, ws := range streams {
		ws.Reset()
	}
}

// --- Worker stream handler (I5: transferID routing, C5: peer ID verification) ---

// handleWorkerStreamFromReader processes an incoming parallel worker stream.
// The caller has already peeked the first byte (msgWorkerHello) via a bufio.Reader.
// All reads go through r to avoid losing the buffered data.
//
// Security (C5): Verifies that the worker stream's peer ID matches the control
// stream's peer ID. This prevents session hijack where a malicious peer copies
// a transferID from observed traffic and opens a worker stream to inject chunks.
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

	transferID, err := readWorkerHello(r)
	if err != nil {
		slog.Debug("file-transfer: worker transferID read failed", "peer", short, "error", err)
		return
	}

	// Look up session by transferID (I5).
	ts.mu.RLock()
	session, ok := ts.parallelSessions[transferID]
	ts.mu.RUnlock()

	if !ok {
		slog.Debug("file-transfer: no session for worker stream", "peer", short)
		return
	}

	// C5: Verify worker peer ID matches control stream peer ID.
	// Prevents session hijack from a different peer.
	if remotePeer != session.controlPID {
		slog.Warn("file-transfer: worker stream peer mismatch (C5)",
			"worker_peer", short,
			"control_peer", session.controlPID.String()[:16]+"...")
		return
	}

	// Assign a stream index (workers start at 1, control is 0).
	// Limit max worker streams to prevent resource exhaustion from malicious senders.
	streamIdx := int(atomic.AddInt32(&session.nextWorkerID, 1))
	if streamIdx > parallelStreamsLANMax {
		slog.Warn("file-transfer: too many worker streams, rejecting",
			"peer", short, "stream", streamIdx, "max", parallelStreamsLANMax)
		return
	}

	// D1 fix: register this worker stream so cancel can reset it.
	session.mu.Lock()
	session.workerStreams = append(session.workerStreams, s)
	session.mu.Unlock()

	// Set deadline on worker stream to prevent slowloris (silent worker holds goroutine).
	// Same deadline as control stream. resetWorkerStreams will override this if the
	// transfer completes earlier.
	s.SetDeadline(time.Now().Add(transferStreamDeadline))

	slog.Debug("file-transfer: worker stream attached", "peer", short, "stream", streamIdx)

	// Read streaming chunks and deliver to session (I12: readStreamChunkFrame).
	for {
		select {
		case <-session.done:
			return
		default:
		}

		sc, msgType, readErr := readStreamChunkFrame(r)
		if readErr != nil {
			return // stream closed or error
		}

		// Workers should only send chunk frames.
		// Trailer/done signals on worker streams are unexpected but handled gracefully.
		if msgType != msgStreamChunk {
			return
		}

		// Deliver chunk to session via select to avoid goroutine leak if
		// receiveParallel has returned and the channel is full.
		select {
		case session.chunks <- sc:
		case <-session.done:
			return
		}
	}
}

// --- Session registry (I5: transferID key) ---

// registerParallelSession registers a session for worker streams to find.
func (ts *TransferService) registerParallelSession(transferID [32]byte, session *parallelSession) {
	ts.mu.Lock()
	if ts.parallelSessions == nil {
		ts.parallelSessions = make(map[[32]byte]*parallelSession)
	}
	ts.parallelSessions[transferID] = session
	ts.mu.Unlock()
}

// unregisterParallelSession removes a session.
func (ts *TransferService) unregisterParallelSession(transferID [32]byte) {
	ts.mu.Lock()
	delete(ts.parallelSessions, transferID)
	ts.mu.Unlock()
}

// --- Parallel receive (streaming protocol) ---

// receiveParallel reads streaming chunks from both the control stream and parallel
// worker streams. It coordinates writes from multiple sources using streamReceiveState.
//
// The control stream uses readStreamChunkFrame (I12). Worker streams deliver chunks
// via the session.chunks channel. All chunks are processed through streamReceiveState
// for duplicate detection (R3-IMP3), bounds validation (C3/C4), hash verification,
// and globalToLocal file mapping (N3/F1).
func (ts *TransferService) receiveParallel(
	controlReader io.Reader,
	session *parallelSession,
) ([32]byte, error) {
	var zero [32]byte
	state := session.state
	progress := session.progress

	// Checkpoint state: periodic save to survive crashes (N10, R3-IMP5).
	ckptEnabled := session.receiveDir != ""
	lastCkptSave := time.Now()

	// saveCheckpointIfDue saves a checkpoint if enough time has elapsed since the last save.
	saveCheckpointIfDue := func() {
		if !ckptEnabled {
			return
		}
		if time.Since(lastCkptSave) < checkpointSaveInterval {
			return
		}
		ckpt := checkpointFromState(state, session.contentKey, session.flags)
		if err := ckpt.save(session.receiveDir); err != nil {
			slog.Debug("file-transfer: checkpoint save failed", "error", err)
		}
		lastCkptSave = time.Now()
	}

	// saveCheckpointOnError persists final state before returning an error.
	// On success, sets keepTempFiles so cleanup() preserves partial data for resume.
	saveCheckpointOnError := func() {
		if !ckptEnabled {
			return
		}
		ckpt := checkpointFromState(state, session.contentKey, session.flags)
		if err := ckpt.save(session.receiveDir); err != nil {
			slog.Debug("file-transfer: checkpoint save on error failed", "error", err)
			return
		}
		// Checkpoint saved successfully. Tell cleanup to preserve temp files
		// so the next session can resume from the checkpoint.
		state.mu.Lock()
		state.keepTempFiles = true
		state.mu.Unlock()
	}

	// Process a single streaming chunk from any source.
	// Uses the shared processIncomingChunk on streamReceiveState (Q1 fix).
	processChunk := func(sc streamChunk) error {
		progress.addWireBytes(int64(len(sc.data)))

		isNew, err := state.processIncomingChunk(sc)
		if err != nil {
			return err
		}
		if !isNew {
			return nil
		}

		progress.updateChunks(state.ReceivedBytes(), sc.chunkIdx+1)

		// Periodic checkpoint save (N10).
		saveCheckpointIfDue()

		return nil
	}

	// Ensure worker cleanup happens on ALL exit paths.
	var cleanupOnce sync.Once
	cleanupWorkers := func() {
		cleanupOnce.Do(func() {
			close(session.done)
			session.resetWorkerStreams()
		})
	}
	defer cleanupWorkers()

	// Control stream goroutine: read streaming chunks until trailer.
	type controlResult struct {
		chunkCount   int
		rootHash     [32]byte
		sparseHashes map[int][32]byte
		erasure      *erasureTrailer
		err          error
	}
	controlDone := make(chan controlResult, 1)
	go func() {
		for {
			sc, msgType, readErr := readStreamChunkFrame(controlReader)
			if readErr != nil {
				controlDone <- controlResult{err: fmt.Errorf("control read: %w", readErr)}
				return
			}

			switch msgType {
			case msgTrailer:
				chunkCount, rootHash, sparseHashes, erasure, trailerErr := readTrailer(controlReader, state.hasErasure)
				if trailerErr != nil {
					controlDone <- controlResult{err: fmt.Errorf("read trailer: %w", trailerErr)}
					return
				}
				controlDone <- controlResult{
					chunkCount:   chunkCount,
					rootHash:     rootHash,
					sparseHashes: sparseHashes,
					erasure:      erasure,
				}
				return

			case msgTransferDone:
				controlDone <- controlResult{err: fmt.Errorf("unexpected msgTransferDone in streaming parallel protocol")}
				return

			case msgStreamChunk:
				if err := processChunk(sc); err != nil {
					controlDone <- controlResult{err: err}
					return
				}
			}
		}
	}()

	// Process chunks from worker streams and control stream concurrently.
	var ctrlResult controlResult
	for {
		select {
		case sc := <-session.chunks:
			if err := processChunk(sc); err != nil {
				cleanupWorkers()
				saveCheckpointOnError()
				// Don't block on controlDone (S2 fix): control goroutine will exit
				// when the caller closes the stream. Buffered channel prevents leak.
				return zero, err
			}

		case result := <-controlDone:
			if result.err != nil {
				cleanupWorkers()
				saveCheckpointOnError()
				return zero, result.err
			}
			ctrlResult = result

			// Control stream finished (trailer received). Signal workers to stop,
			// then drain remaining chunks.
			cleanupWorkers()

			// Only drain if workers actually attached. Without this check,
			// every single-stream transfer pays a 50ms timer penalty.
			if atomic.LoadInt32(&session.nextWorkerID) > 0 {
				// Workers may have in-flight chunks between readStreamChunkFrame
				// completing and the select on session.done. Brief grace period
				// lets them deliver their last chunk.
				drainTimer := time.NewTimer(50 * time.Millisecond)
			drainLoop:
				for {
					select {
					case sc := <-session.chunks:
						if err := processChunk(sc); err != nil {
							drainTimer.Stop()
							saveCheckpointOnError()
							return zero, err
						}
					case <-drainTimer.C:
						break drainLoop
					}
				}
			}
			goto verify
		}
	}

verify:
	// Verify transfer integrity.
	chunkCount := ctrlResult.chunkCount
	rootHash := ctrlResult.rootHash

	// Update progress with actual chunk count (I3).
	progress.mu.Lock()
	progress.ChunksTotal = chunkCount
	progress.mu.Unlock()

	// Check for missing chunks (R3-IMP4).
	missing := state.missingChunks(chunkCount)
	if len(missing) > 0 {
		saveCheckpointOnError()
		return zero, fmt.Errorf("transfer incomplete: %d chunks missing (first: %d)", len(missing), missing[0])
	}

	// Assemble full hash list and verify Merkle root.
	var allHashes [][32]byte
	if len(ctrlResult.sparseHashes) > 0 {
		allHashes = state.assembleFullHashList(chunkCount, ctrlResult.sparseHashes)
	} else {
		allHashes = state.orderedHashes()
	}
	computedRoot := MerkleRoot(allHashes)
	if computedRoot != rootHash {
		saveCheckpointOnError()
		return zero, fmt.Errorf("Merkle root mismatch: transfer corrupted")
	}

	// Set erasure info on progress.
	if ctrlResult.erasure != nil {
		progress.mu.Lock()
		progress.ErasureParity = ctrlResult.erasure.ParityCount
		if chunkCount > 0 {
			progress.ErasureOverhead = float64(ctrlResult.erasure.ParityCount) / float64(chunkCount)
		}
		progress.mu.Unlock()
	}

	// Finalize all temp files.
	if finalizeErr := state.finalize(); finalizeErr != nil {
		saveCheckpointOnError()
		return zero, fmt.Errorf("finalize: %w", finalizeErr)
	}

	// Transfer succeeded. Remove checkpoint (N10).
	if ckptEnabled {
		removeCheckpoint(session.receiveDir, session.contentKey)
	}

	return rootHash, nil
}

