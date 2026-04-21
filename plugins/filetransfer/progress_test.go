package filetransfer

import (
	"math"
	"testing"
	"time"
	"unicode/utf8"
)

// R4-F1: progress code had ZERO test coverage. These tests cover the
// speedEstimator, formatETA, adaptive width, and humanSize edge cases.

func TestSpeedEstimator_Basic(t *testing.T) {
	var est speedEstimator
	start := time.Now()

	// Simulate 6 ticks at 500ms intervals, transferring 1 MB per tick.
	for i := 1; i <= 6; i++ {
		est.update(int64(i)*1_000_000, start.Add(time.Duration(i)*500*time.Millisecond))
	}

	spd := est.speed()
	if spd < 1_900_000 || spd > 2_100_000 {
		t.Errorf("expected speed ~2 MB/s, got %.0f", spd)
	}

	eta := est.eta(10_000_000) // 10 MB remaining
	if eta < 4.0 || eta > 6.0 {
		t.Errorf("expected ETA ~5s, got %.1f", eta)
	}
}

func TestSpeedEstimator_NaN(t *testing.T) {
	var est speedEstimator
	start := time.Now()

	est.update(1_000_000, start.Add(500*time.Millisecond))
	est.update(2_000_000, start.Add(1000*time.Millisecond))

	spd := est.speed()
	if math.IsNaN(spd) || math.IsInf(spd, 0) {
		t.Errorf("speed is NaN/Inf: %v", spd)
	}

	eta := est.eta(5_000_000)
	if math.IsNaN(eta) || math.IsInf(eta, 0) {
		t.Errorf("eta is NaN/Inf: %v", eta)
	}
}

func TestSpeedEstimator_Stall(t *testing.T) {
	var est speedEstimator
	start := time.Now()

	// Transfer data for first 2 ticks.
	est.update(1_000_000, start.Add(500*time.Millisecond))
	est.update(2_000_000, start.Add(1000*time.Millisecond))

	// Stall for 4 seconds to push active period out of the 3s window.
	for i := 3; i <= 10; i++ {
		est.update(2_000_000, start.Add(time.Duration(i)*500*time.Millisecond))
	}

	if est.rawSpeed != 0 {
		t.Errorf("expected rawSpeed=0 during stall, got %.0f", est.rawSpeed)
	}
	if eta := est.eta(5_000_000); eta != -1 {
		t.Errorf("expected eta=-1 during stall, got %.1f", eta)
	}
}

func TestSpeedEstimator_ResumeAfterStall(t *testing.T) {
	var est speedEstimator
	start := time.Now()

	// Active: 1 MB/tick for 6 ticks = 2 MB/s.
	for i := 1; i <= 6; i++ {
		est.update(int64(i)*1_000_000, start.Add(time.Duration(i)*500*time.Millisecond))
	}

	// Build ETA context at 2 MB/s.
	eta1 := est.eta(10_000_000)
	if eta1 < 0 {
		t.Fatalf("expected positive ETA before stall, got %.1f", eta1)
	}

	// Stall: 8 ticks push active period out of 3s window.
	for i := 7; i <= 14; i++ {
		est.update(6_000_000, start.Add(time.Duration(i)*500*time.Millisecond))
	}

	// During stall, eta should be -1.
	if eta := est.eta(10_000_000); eta != -1 {
		t.Errorf("expected ETA -1 during stall, got %.1f", eta)
	}

	// Resume at lower speed: 500 KB/tick. 3 ticks so stall ticks still in window.
	for i := 15; i <= 17; i++ {
		est.update(6_000_000+int64(i-14)*500_000, start.Add(time.Duration(i)*500*time.Millisecond))
	}

	// ETA should reflect current speed, not stale pre-stall estimate.
	// Without smoothETA reset: eta blends with stale ~5s -> ~6.5s (too low).
	// With reset: eta seeds fresh from current rawSpeed -> ~20s (correct).
	eta2 := est.eta(10_000_000)
	if eta2 < 7.0 {
		t.Errorf("ETA %.1f too low after resume (stale smoothETA not reset?)", eta2)
	}
}

func TestSpeedEstimator_Seed(t *testing.T) {
	var est speedEstimator
	start := time.Now()

	// First two ticks: no data.
	est.update(0, start.Add(500*time.Millisecond))
	est.update(0, start.Add(1000*time.Millisecond))

	if est.initialized {
		t.Error("expected not initialized with zero data")
	}

	// Data arrives: R2-F4 says first speed seeds directly, not blending with 0.
	est.update(1_000_000, start.Add(1500*time.Millisecond))
	est.update(2_000_000, start.Add(2000*time.Millisecond))

	if est.smoothSpeed <= 0 {
		t.Errorf("expected positive smoothSpeed after seed, got %.0f", est.smoothSpeed)
	}
}

func TestFormatETA(t *testing.T) {
	tests := []struct {
		sec  float64
		want string
	}{
		{-1, ""},
		{math.NaN(), ""},
		{math.Inf(1), ""},
		{0, "00:00"},
		{30, "00:30"},
		{90, "01:30"},
		{3661, "1:01:01"},
		{360000, ">24h"},
		{500000, ">24h"},
	}
	for _, tt := range tests {
		got := formatETA(tt.sec)
		if got != tt.want {
			t.Errorf("formatETA(%v) = %q, want %q", tt.sec, got, tt.want)
		}
	}
}

func TestBuildProgressLine_AdaptiveWidth(t *testing.T) {
	tests := []struct {
		name string
		tw   int
	}{
		{"wide_160", 160},
		{"standard_120", 120},
		{"narrow_80", 80},
		{"very_narrow_40", 40},
		{"ultra_narrow_25", 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := buildProgressLine(tt.tw, 0.428, 8_000_000_000, 6_700_000,
				432.0, 1850, 4321, true, 500_000_000,
				432, 0.10, 43, false)
			displayWidth := utf8.RuneCountInString(line)
			if displayWidth > tt.tw {
				t.Errorf("display width %d exceeds terminal width %d: %q", displayWidth, tt.tw, line)
			}
			if tt.tw >= 20 && line == "" {
				t.Errorf("expected non-empty line for tw=%d", tt.tw)
			}
		})
	}
}

func TestBuildProgressLine_QuietMode(t *testing.T) {
	full := buildProgressLine(160, 0.50, 1_000_000_000, 5_000_000,
		300.0, 500, 1000, true, 400_000_000,
		100, 0.10, 10, false)
	quiet := buildProgressLine(160, 0.50, 1_000_000_000, 5_000_000,
		300.0, 500, 1000, true, 400_000_000,
		100, 0.10, 10, true)
	if len(quiet) >= len(full) {
		t.Errorf("quiet line (%d) should be shorter than full (%d)", len(quiet), len(full))
	}
}

func TestHumanSize_Negative(t *testing.T) {
	got := humanSize(-1)
	if got != "0 B" {
		t.Errorf("humanSize(-1) = %q, want %q", got, "0 B")
	}
}

func TestTruncateDisplay(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"short", 20, "short"},
		{"exactly-twenty-chars", 20, "exactly-twenty-chars"},
		{"this-is-a-very-long-filename.bin", 20, "this-is-a-very-lo..."},
		// Multi-byte: CJK characters are 3 bytes each. Byte-based [:17] would
		// split a character mid-sequence producing invalid UTF-8. Rune-based
		// truncation must produce valid output.
		{"日本語のファイル名テスト.bin", 20, "日本語のファイル名テスト.bin"},           // 16 runes, fits
		{"日本語のファイル名テストファイル名が長い.bin", 20, "日本語のファイル名テストファイル名..."},  // 24 runes -> 17+...
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
		{"", 20, ""},
		{"hello", 0, ""},  // zero max
		{"hello", -1, ""}, // negative max (panic guard)
	}
	for _, tt := range tests {
		got := truncateDisplay(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncateDisplay(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}
