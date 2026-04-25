package filetransfer

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

)

// containsShellMeta returns true if the string contains shell metacharacters
// that could allow command injection (P8 fix).
func containsShellMeta(s string) bool {
	const meta = ";|&$`\\!#()\n\r"
	return strings.ContainsAny(s, meta)
}

// sanitizeNotifyCommand returns the command if safe, empty string if dangerous (P8 fix).
func sanitizeNotifyCommand(cmd string) string {
	if cmd == "" {
		return ""
	}
	if containsShellMeta(cmd) {
		slog.Warn("plugin.filetransfer: notify_command contains shell metacharacters, ignoring", "command", cmd)
		return ""
	}
	return cmd
}

// PluginConfig holds the plugin's configuration, loaded from its own config.yaml.
type PluginConfig struct {
	ReceiveDir      string  `yaml:"receive_dir"`
	MaxFileSize     int64   `yaml:"max_file_size"`
	ReceiveMode     string  `yaml:"receive_mode"`     // off, contacts, ask, open, timed
	TimedDuration   string  `yaml:"timed_duration"`    // e.g. "10m"
	Compress        *bool   `yaml:"compress"`
	Notify          string  `yaml:"notify"`          // "none", "desktop", "command"
	NotifyCommand   string  `yaml:"notify_command"`
	LogPath         string  `yaml:"log_path"`
	MaxConcurrent   int     `yaml:"max_concurrent"`
	RateLimit       int     `yaml:"rate_limit"`
	BrowseRateLimit int     `yaml:"browse_rate_limit"`
	QueueFile       string  `yaml:"queue_file"`

	// Multi-peer swarming.
	MultiPeerEnabled       *bool  `yaml:"multi_peer_enabled"`
	MultiPeerMaxPeers      int    `yaml:"multi_peer_max_peers"`
	MultiPeerMinSize       int64  `yaml:"multi_peer_min_size"`
	MaxServedBytesPerHour  string `yaml:"max_served_bytes_per_hour"` // human-readable: "10GB", "unlimited", or plain bytes (0 = unlimited)

	// Erasure coding.
	ErasureOverhead *float64 `yaml:"erasure_overhead"`

	// Inbound capacity.
	MaxInboundTransfers int `yaml:"max_inbound_transfers"` // global concurrent inbound limit (default: 20)
	MaxPerPeerTransfers int `yaml:"max_per_peer_transfers"` // per-peer concurrent inbound limit (default: 5)

	// DDoS defenses.
	GlobalRateLimit  int   `yaml:"global_rate_limit"`
	MaxQueuedPerPeer int   `yaml:"max_queued_per_peer"`
	MinSpeedBytes    int   `yaml:"min_speed_bytes"`
	MinSpeedSeconds int    `yaml:"min_speed_seconds"`
	MaxTempSize     int64  `yaml:"max_temp_size"`
	TempFileExpiry  string `yaml:"temp_file_expiry"`
	BandwidthBudget string `yaml:"bandwidth_budget"` // human-readable: "500MB", "1GB", "unlimited", or plain bytes
	SendRateLimit   string `yaml:"send_rate_limit"`  // max send rate bytes/sec: "100M", "500M", "0" = unlimited

	// Share defaults.
	DefaultPersistent *bool `yaml:"default_persistent"` // default for --persist flag (default: true)

	// Failure backoff.
	FailureBackoff struct {
		Threshold int    `yaml:"threshold"`
		Window    string `yaml:"window"`
		Block     string `yaml:"block"`
	} `yaml:"failure_backoff"`
}

// loadConfig parses the plugin config from raw YAML bytes.
// Returns defaults if bytes are empty or nil. Logs warning on parse errors (P21 fix).
func loadConfig(data []byte) PluginConfig {
	var cfg PluginConfig
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			slog.Warn("plugin.filetransfer: config parse error, using defaults", "error", err)
		}
	}
	// Apply defaults.
	if cfg.ReceiveMode == "" {
		cfg.ReceiveMode = "contacts"
	}
	return cfg
}

// defaultPersistent returns the configured default for share persistence.
// True unless explicitly set to false in config.
func (c *PluginConfig) defaultPersistent() bool {
	return c.DefaultPersistent == nil || *c.DefaultPersistent
}

// reloadConfig implements the 6-field hot-reload with rollback logic.
// This replicates the configReloader.ReloadConfig() transfer section from cmd_daemon.go.
func (p *FileTransferPlugin) reloadConfig(newBytes []byte) {
	newCfg := loadConfig(newBytes)

	p.mu.Lock()
	oldCfg := p.config
	ts := p.transferService
	p.mu.Unlock()
	if ts == nil {
		p.mu.Lock()
		p.config = newCfg
		p.mu.Unlock()
		return
	}

	type rollbackEntry struct {
		field   string
		restore func()
	}
	var applied []rollbackEntry

	rollbackAll := func() {
		for i := len(applied) - 1; i >= 0; i-- {
			applied[i].restore()
			slog.Warn("plugin.config-rollback", "plugin", "filetransfer", "field", applied[i].field)
		}
		// Restore old config.
		p.mu.Lock()
		p.config = oldCfg
		p.mu.Unlock()
	}

	// 1. Receive mode.
	oldMode := oldCfg.ReceiveMode
	newMode := newCfg.ReceiveMode
	if oldMode == "" {
		oldMode = "contacts"
	}
	if newMode == "" {
		newMode = "contacts"
	}
	if oldMode != newMode {
		if newMode == "timed" {
			durStr := newCfg.TimedDuration
			if durStr == "" {
				durStr = "10m"
			}
			dur, err := time.ParseDuration(durStr)
			if err != nil {
				slog.Warn("plugin.config-reload: invalid timed_duration", "plugin", "filetransfer", "value", durStr, "error", err)
				rollbackAll()
				return
			}
			if err := ts.SetTimedMode(dur); err != nil {
				slog.Warn("plugin.config-reload: timed mode failed", "plugin", "filetransfer", "error", err)
				rollbackAll()
				return
			}
		} else {
			// Validate mode value.
			switch newMode {
			case "off", "contacts", "ask", "open":
			default:
				slog.Warn("plugin.config-reload: invalid receive_mode", "plugin", "filetransfer", "value", newMode)
				rollbackAll()
				return
			}
			ts.SetReceiveMode(ReceiveMode(newMode))
		}
		applied = append(applied, rollbackEntry{
			field:   "receive_mode",
			restore: func() { ts.SetReceiveMode(ReceiveMode(oldMode)) },
		})
	}

	// 2. Receive directory.
	if oldCfg.ReceiveDir != newCfg.ReceiveDir && newCfg.ReceiveDir != "" {
		ts.SetReceiveDir(newCfg.ReceiveDir)
		oldDir := oldCfg.ReceiveDir
		applied = append(applied, rollbackEntry{
			field:   "receive_dir",
			restore: func() { ts.SetReceiveDir(oldDir) },
		})
	}

	// 3. Max file size.
	if oldCfg.MaxFileSize != newCfg.MaxFileSize {
		ts.SetMaxSize(newCfg.MaxFileSize)
		oldMax := oldCfg.MaxFileSize
		applied = append(applied, rollbackEntry{
			field:   "max_file_size",
			restore: func() { ts.SetMaxSize(oldMax) },
		})
	}

	// 4. Compress.
	oldCompress := oldCfg.Compress == nil || *oldCfg.Compress
	newCompress := newCfg.Compress == nil || *newCfg.Compress
	if oldCompress != newCompress {
		ts.SetCompress(newCompress)
		applied = append(applied, rollbackEntry{
			field:   "compress",
			restore: func() { ts.SetCompress(oldCompress) },
		})
	}

	// 5. Notify mode.
	if oldCfg.Notify != newCfg.Notify && newCfg.Notify != "" {
		ts.SetNotifyMode(newCfg.Notify)
		oldNotify := oldCfg.Notify
		applied = append(applied, rollbackEntry{
			field:   "notify",
			restore: func() { ts.SetNotifyMode(oldNotify) },
		})
	}

	// 6. Notify command (P8 fix: reject shell metacharacters).
	if oldCfg.NotifyCommand != newCfg.NotifyCommand {
		if newCfg.NotifyCommand != "" && containsShellMeta(newCfg.NotifyCommand) {
			slog.Warn("plugin.config-reload: notify_command contains shell metacharacters", "plugin", "filetransfer")
			rollbackAll()
			return
		}
		ts.SetNotifyCommand(newCfg.NotifyCommand)
		oldCmd := oldCfg.NotifyCommand
		applied = append(applied, rollbackEntry{
			field:   "notify_command",
			restore: func() { ts.SetNotifyCommand(oldCmd) },
		})
	}

	p.mu.Lock()
	p.config = newCfg
	p.mu.Unlock()

	if len(applied) > 0 {
		fields := make([]string, len(applied))
		for i, a := range applied {
			fields[i] = a.field
		}
		slog.Info("plugin.config-reloaded", "plugin", "filetransfer", "changed", fmt.Sprintf("%v", fields))
	}
}
