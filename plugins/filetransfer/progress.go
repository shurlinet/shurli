package filetransfer

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

// humanSize formats bytes into a human-readable string.
// Consolidated from cmd_send.go humanSize() and handlers.go humanSizeAPI().
func humanSize(b int64) string {
	if b < 0 { // R2-SEC-9: clamp negative values from daemon bugs.
		b = 0
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// termWidth returns the terminal width, defaulting to 80 if detection fails.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// progressMode describes how progress output should be rendered.
type progressMode int

const (
	progressANSI  progressMode = iota // standard TTY: \r + \033[K (erase line)
	progressPlain                     // pipe/file: milestone lines only
)

// detectProgressMode checks the terminal environment.
func detectProgressMode() progressMode {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return progressPlain
	}
	return progressANSI
}

// progressClear returns the escape sequence to clear the current line.
func progressClear() string {
	return "\r\033[K"
}

// --- Speed estimation (yt-dlp-style sliding window + EWMA) ---

// speedSample is a timestamped byte counter for the sliding window.
type speedSample struct {
	t     time.Time
	bytes int64
}

// speedEstimator computes smoothed transfer speed and ETA using a 3-second
// sliding window plus EWMA. Single-goroutine only; no mutex needed (R2-SEC-6).
type speedEstimator struct {
	samples     []speedSample
	rawSpeed    float64   // latest raw windowed speed (bytes/sec)
	smoothSpeed float64   // EWMA alpha=0.3 (for display)
	smoothETA   float64   // EWMA alpha=0.1 (seconds, double-smoothed)
	graceStart  time.Time // when first data byte arrived
	initialized bool      // has received at least one non-zero speed
}

const (
	speedWindowDuration = 3 * time.Second
	speedAlpha          = 0.3
	etaAlpha            = 0.1
)

// update adds a sample and recomputes speed. Must be called every tick
// (R2-F1: always add sample even when bytes unchanged, so stall is detected).
func (e *speedEstimator) update(transferred int64, now time.Time) {
	e.samples = append(e.samples, speedSample{t: now, bytes: transferred})

	if transferred > 0 && e.graceStart.IsZero() {
		e.graceStart = now
	}

	// Prune samples older than window, keeping at least the oldest in-window.
	cutoff := now.Add(-speedWindowDuration)
	start := 0
	for start < len(e.samples)-1 && e.samples[start].t.Before(cutoff) {
		start++
	}
	if start > 0 {
		n := copy(e.samples, e.samples[start:])
		e.samples = e.samples[:n]
	}

	if len(e.samples) < 2 {
		return
	}

	oldest := e.samples[0]
	newest := e.samples[len(e.samples)-1]
	dt := newest.t.Sub(oldest.t).Seconds()

	if dt < 0.1 { // R2-F5: prevent enormous values on tiny window.
		return
	}

	bytesDelta := float64(newest.bytes - oldest.bytes)
	if bytesDelta < 0 {
		bytesDelta = 0
	}
	raw := bytesDelta / dt

	if math.IsNaN(raw) || math.IsInf(raw, 0) { // R2-SEC-8
		return
	}

	e.rawSpeed = raw

	// Reset ETA smoothing when speed drops to zero (stall). Pre-stall ETA
	// context is invalidated; fresh seed on resume gives immediately accurate ETA.
	if raw == 0 && e.smoothETA > 0 {
		e.smoothETA = 0
	}

	if !e.initialized && raw > 0 { // R2-F4: seed directly, don't blend with 0.
		e.smoothSpeed = raw
		e.initialized = true
	} else if e.initialized {
		e.smoothSpeed = (1-speedAlpha)*e.smoothSpeed + speedAlpha*raw
		if math.IsNaN(e.smoothSpeed) || math.IsInf(e.smoothSpeed, 0) { // R2-SEC-8
			e.smoothSpeed = raw
		}
	}
}

// speed returns the EWMA-smoothed speed in bytes/sec, or 0 if unknown.
func (e *speedEstimator) speed() float64 {
	if !e.initialized {
		return 0
	}
	return e.smoothSpeed
}

// eta returns the EWMA-smoothed ETA in seconds, or -1 if unknown.
// Double-smoothed: raw windowed speed -> raw ETA -> EWMA ETA.
func (e *speedEstimator) eta(remaining int64) float64 {
	if !e.initialized || e.rawSpeed <= 0 || remaining <= 0 {
		return -1
	}
	rawETA := float64(remaining) / e.rawSpeed
	if math.IsNaN(rawETA) || math.IsInf(rawETA, 0) || rawETA > 360000 { // F17/SEC-7
		return -1
	}
	if e.smoothETA <= 0 {
		e.smoothETA = rawETA
	} else {
		e.smoothETA = (1-etaAlpha)*e.smoothETA + etaAlpha*rawETA
		if math.IsNaN(e.smoothETA) || math.IsInf(e.smoothETA, 0) { // R2-SEC-8
			e.smoothETA = rawETA
		}
	}
	return e.smoothETA
}

// isGracePeriod returns true if < 1s since first data byte (F20).
// Not called in the current CLI progress display (which has no "stalled" text),
// but part of the speedEstimator public API for Phase 15 TUI (R4-F9) where
// bubbletea will instantiate its own estimator and needs grace-period awareness.
func (e *speedEstimator) isGracePeriod() bool {
	return e.graceStart.IsZero() || time.Since(e.graceStart) < time.Second
}

// --- Formatting helpers ---

// truncateDisplay truncates a string to max display runes, appending "..." if
// truncated. Rune-safe: never breaks multi-byte UTF-8 characters mid-sequence.
func truncateDisplay(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// formatETA formats seconds into MM:SS or HH:MM:SS. Returns "" if unknown.
func formatETA(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return ""
	}
	s := int(seconds)
	if s >= 360000 { // > ~100 hours
		return ">24h"
	}
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// buildProgressLine constructs the adaptive-width progress string for the live
// in-place display. No ANSI color codes in the output (R3-F1: prevents width
// miscalculation since len() counts invisible escape bytes).
func buildProgressLine(tw int, pct float64, totalSize int64, speedBps float64, etaSec float64,
	chunksDone, chunksTotal int, compressed bool, compressedSize int64,
	erasureParity int, erasureOverhead float64, parityDone int, quiet bool) string {

	if tw < 20 { // R2-F12: too narrow, caller should use plain mode.
		return ""
	}

	speedStr := "--"
	if speedBps > 0 {
		speedStr = humanSize(int64(speedBps)) + "/s"
	}

	// R2-F12: ultra-narrow abbreviated format.
	if tw < 40 {
		core := fmt.Sprintf("%.1f%% %s", pct*100, speedStr)
		if etaFmt := formatETA(etaSec); etaFmt != "" {
			withETA := core + " ETA " + etaFmt
			if 2+len(withETA) <= tw {
				core = withETA
			}
		}
		return "  " + core
	}

	// Core metrics (always shown).
	core := fmt.Sprintf("%.1f%% of %s at %s", pct*100, humanSize(totalSize), speedStr)
	if etaFmt := formatETA(etaSec); etaFmt != "" {
		core += " ETA " + etaFmt
	}

	// Optional segments, priority 1 (chunk) -> 2 (zstd) -> 3 (RS).
	// R2-F16: quiet mode suppresses all optional segments.
	var segments []string
	if !quiet && chunksTotal > 0 {
		seg := fmt.Sprintf("(chunk %d/%d)", chunksDone, chunksTotal)
		if erasureParity > 0 {
			seg = fmt.Sprintf("(chunk %d/%d +%d/%d parity)", chunksDone, chunksTotal, parityDone, erasureParity)
		}
		segments = append(segments, seg)
	}
	if !quiet && compressed && compressedSize > 0 && totalSize > 0 {
		ratio := float64(totalSize) / float64(compressedSize)
		segments = append(segments, fmt.Sprintf("[zstd %.1f:1]", ratio))
	}
	if !quiet && erasureParity > 0 {
		segments = append(segments, fmt.Sprintf("[RS %.0f%%, %d parity]", erasureOverhead*100, erasureParity))
	}

	// Compute bar width from remaining space. R2-F10: dynamic, not fixed.
	available := tw - 2 // leading "  "
	contentLen := len(core)
	for _, seg := range segments {
		contentLen += 1 + len(seg)
	}
	barWidth := available - contentLen - 2 // 2-char gap between bar and content

	// Drop segments right-to-left until bar >= 8.
	for barWidth < 8 && len(segments) > 0 {
		dropped := segments[len(segments)-1]
		segments = segments[:len(segments)-1]
		barWidth += 1 + len(dropped)
	}

	if barWidth < 8 {
		barWidth = 0 // no bar, just metrics
	}
	if barWidth > 60 {
		barWidth = 60
	}

	var sb strings.Builder
	sb.WriteString("  ")
	if barWidth > 0 {
		filled := int(pct * float64(barWidth))
		if filled < 0 {
			filled = 0
		}
		if filled > barWidth {
			filled = barWidth
		}
		sb.WriteString(strings.Repeat("\u2588", filled))
		sb.WriteString(strings.Repeat("\u2591", barWidth-filled))
		sb.WriteString("  ")
	}
	sb.WriteString(core)
	for _, seg := range segments {
		sb.WriteByte(' ')
		sb.WriteString(seg)
	}
	return sb.String()
}

// buildCompletionLine constructs the single-line completion string. Uses the
// same adaptive width as the live line (R3-F6) with "in MM:SS" replacing
// "ETA MM:SS" and "avg" suffix on speed (R3-F7).
func buildCompletionLine(tw int, totalSize int64, durSec float64, avgSpeed float64,
	compressed bool, compressedSize int64, erasureParity int, erasureOverhead float64,
	streams int, failovers int) string {

	durFmt := formatETA(durSec)
	if durFmt == "" {
		durFmt = "00:00"
	}
	core := fmt.Sprintf("100%% of %s in %s at %s/s avg",
		humanSize(totalSize), durFmt, humanSize(int64(avgSpeed)))

	var segments []string
	if compressed && compressedSize > 0 && totalSize > 0 {
		ratio := float64(totalSize) / float64(compressedSize)
		segments = append(segments, fmt.Sprintf("[zstd %.1f:1]", ratio))
	}
	if erasureParity > 0 {
		segments = append(segments, fmt.Sprintf("[RS %.0f%%, %d parity]", erasureOverhead*100, erasureParity))
	}
	if streams > 1 {
		segments = append(segments, fmt.Sprintf("[%d streams]", streams))
	}
	if failovers > 0 { // R4-F8: failover count in completion only.
		if failovers == 1 {
			segments = append(segments, "[1 failover]")
		} else {
			segments = append(segments, fmt.Sprintf("[%d failovers]", failovers))
		}
	}

	available := tw - 2
	contentLen := len(core)
	for _, seg := range segments {
		contentLen += 1 + len(seg)
	}
	barWidth := available - contentLen - 2

	for barWidth < 8 && len(segments) > 0 {
		dropped := segments[len(segments)-1]
		segments = segments[:len(segments)-1]
		barWidth += 1 + len(dropped)
	}
	if barWidth < 8 {
		barWidth = 0
	}
	if barWidth > 60 {
		barWidth = 60
	}

	var sb strings.Builder
	sb.WriteString("  ")
	if barWidth > 0 {
		sb.WriteString(strings.Repeat("\u2588", barWidth))
		sb.WriteString("  ")
	}
	sb.WriteString(core)
	for _, seg := range segments {
		sb.WriteByte(' ')
		sb.WriteString(seg)
	}
	return sb.String()
}

// --- Poll functions ---

// pollTransferJSON streams NDJSON progress events for --follow --json mode.
func pollTransferJSON(client *daemonClient, id string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	enc := json.NewEncoder(os.Stdout)
	var est speedEstimator // R2-F17: speedEstimator in JSON path too.

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			enc.Encode(map[string]any{"event": "error", "error": err.Error()})
			return
		}

		now := time.Now()

		// R2-SEC-1: defensive guards (consistent with pollTransfer).
		size := progress.Size
		transferred := progress.Transferred
		if size < 0 {
			size = 0
		}
		if transferred < 0 {
			transferred = 0
		}
		if size > 0 && transferred > size {
			transferred = size
		}

		est.update(transferred, now)

		if progress.Done {
			ev := map[string]any{
				"event":           "completed",
				"transfer_id":     progress.ID,
				"transferred":     transferred,
				"size":            size,
				"compressed_size": progress.CompressedSize,
			}
			// R3-F12: enriched completion for AI agent consumers.
			if !progress.StartTime.IsZero() && !progress.EndTime.IsZero() {
				ev["start_time"] = progress.StartTime
				ev["end_time"] = progress.EndTime
				dur := progress.EndTime.Sub(progress.StartTime)
				if dur > 0 {
					ev["duration_ms"] = dur.Milliseconds()
					ev["avg_speed_bytes_per_sec"] = int64(float64(size) / dur.Seconds())
				}
			}
			if progress.Error != "" {
				ev["event"] = "failed"
				ev["error"] = progress.Error
			}
			enc.Encode(ev)
			return
		}

		ev := map[string]any{
			"event":           "progress",
			"transfer_id":     progress.ID,
			"transferred":     transferred,
			"size":            size,
			"chunks_done":     progress.ChunksDone,
			"chunks_total":    progress.ChunksTotal,
			"compressed_size": progress.CompressedSize,
		}
		// F12: computed speed/ETA in JSON events.
		if spd := est.speed(); spd > 0 {
			ev["speed_bytes_per_sec"] = int64(spd)
		}
		if etaSec := est.eta(size - transferred); etaSec >= 0 {
			ev["eta_seconds"] = int(etaSec)
		}
		// [B2-F29, R4-SEC1 Batch 2] Parity progress for agent/JSON consumers.
		if progress.ErasureParity > 0 {
			ev["erasure_parity"] = progress.ErasureParity
			ev["parity_chunks_done"] = progress.ParityChunksDone
		}
		if len(progress.StreamProgress) > 0 {
			ev["stream_progress"] = progress.StreamProgress
		}
		enc.Encode(ev)
	}
}

func pollTransfer(client *daemonClient, id string, quiet bool) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	pmode := detectProgressMode()
	var est speedEstimator
	var headerPrinted bool
	startTime := time.Now() // R2-F19: fallback for negative duration guard.

	// F15: non-TTY state (1% or 5s interval instead of 25% milestones).
	var lastPlainPct float64
	lastPlainTime := time.Now()

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			// R3-F3: clear in-place progress before error to prevent garble.
			if pmode == progressANSI {
				fmt.Print(progressClear())
			}
			fmt.Fprintf(os.Stderr, "Warning: cannot check transfer status: %v\n", err)
			return
		}

		now := time.Now()

		// R2-SEC-1: defensive guards for malformed daemon responses.
		// Must be BEFORE est.update so the estimator gets clamped values.
		size := progress.Size
		transferred := progress.Transferred
		if size < 0 {
			size = 0
		}
		if transferred < 0 {
			transferred = 0
		}
		if size > 0 && transferred > size {
			transferred = size
		}

		est.update(transferred, now)

		if progress.Done {
			if pmode == progressANSI {
				fmt.Print(progressClear())
			}
			if progress.Error != "" {
				tc.Wred(os.Stdout, "Transfer failed: %s\n", progress.Error)
			} else {
				// R2-F13/F9: use transfer's own timestamps for authoritative stats.
				dur := progress.EndTime.Sub(progress.StartTime)
				if dur <= 0 { // R2-F19: negative duration guard.
					dur = time.Since(startTime)
				}
				avgSpeed := float64(0)
				if dur.Seconds() > 0 {
					avgSpeed = float64(size) / dur.Seconds()
				}

				tw := termWidth()
				streams := len(progress.StreamProgress)
				line := buildCompletionLine(tw, size, dur.Seconds(), avgSpeed,
					progress.Compressed, progress.CompressedSize,
					progress.ErasureParity, progress.ErasureOverhead,
					streams, progress.Failovers)
				// R4-F7: green completion bar + metrics on line 1.
				tc.Wgreen(os.Stdout, "%s\n", line)
				// R4-F7: filename on line 2 (visible in scrollback).
				fmt.Printf("  %s  complete\n", SanitizeDisplayName(progress.Filename))
			}
			return
		}

		// R3-F16: phase indicators before data flows.
		if progress.Status == "pending" {
			if pmode == progressANSI {
				fmt.Printf("%s  queued...", progressClear())
			}
			continue
		}
		if size <= 0 {
			if pmode == progressANSI {
				fmt.Printf("%s  connecting...", progressClear())
			}
			continue
		}

		// Header (printed once when size is known).
		if !headerPrinted {
			tc.Wfaint(os.Stdout, "  File: %s (%s)\n",
				SanitizeDisplayName(progress.Filename), humanSize(size))
			mode := "1 stream"
			if len(progress.StreamProgress) > 1 {
				mode = fmt.Sprintf("%d streams", len(progress.StreamProgress))
			}
			if progress.Compressed {
				mode += ", zstd"
			}
			if progress.ErasureParity > 0 {
				mode += fmt.Sprintf(", RS %.0f%% parity", progress.ErasureOverhead*100)
			}
			tc.Wfaint(os.Stdout, "  Mode: %s\n", mode)
			headerPrinted = true
		}

		pct := float64(transferred) / float64(size)
		remaining := size - transferred
		spd := est.speed()
		etaSec := est.eta(remaining)

		if pmode == progressPlain {
			// F15: print every 1% change OR every 5 seconds.
			currentPct := math.Floor(pct * 1000) / 10
			elapsed := now.Sub(lastPlainTime)
			if currentPct >= lastPlainPct+1.0 || elapsed >= 5*time.Second {
				speedFmt := "--"
				if spd > 0 {
					speedFmt = humanSize(int64(spd)) + "/s"
				}
				line := fmt.Sprintf("%.1f%% of %s at %s", pct*100, humanSize(size), speedFmt)
				if etaFmt := formatETA(etaSec); etaFmt != "" {
					line += " ETA " + etaFmt
				}
				if !quiet && progress.ChunksTotal > 0 {
					line += fmt.Sprintf(" (chunk %d/%d)", progress.ChunksDone, progress.ChunksTotal)
				}
				fmt.Println(line)
				lastPlainPct = currentPct
				lastPlainTime = now
			}
		} else {
			tw := termWidth()
			line := buildProgressLine(tw, pct, size, spd, etaSec,
				progress.ChunksDone, progress.ChunksTotal,
				progress.Compressed, progress.CompressedSize,
				progress.ErasureParity, progress.ErasureOverhead,
				progress.ParityChunksDone, quiet)
			if line != "" {
				fmt.Printf("%s%s", progressClear(), line)
			}
		}
	}
}
