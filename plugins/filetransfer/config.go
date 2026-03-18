package filetransfer

import (
	"fmt"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// TransferConfig holds the plugin's configuration, loaded from its own config.yaml.
type TransferConfig struct {
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
	MultiPeerEnabled  *bool `yaml:"multi_peer_enabled"`
	MultiPeerMaxPeers int   `yaml:"multi_peer_max_peers"`
	MultiPeerMinSize  int64 `yaml:"multi_peer_min_size"`

	// Erasure coding.
	ErasureOverhead *float64 `yaml:"erasure_overhead"`

	// DDoS defenses.
	GlobalRateLimit  int   `yaml:"global_rate_limit"`
	MaxQueuedPerPeer int   `yaml:"max_queued_per_peer"`
	MinSpeedBytes    int   `yaml:"min_speed_bytes"`
	MinSpeedSeconds int    `yaml:"min_speed_seconds"`
	MaxTempSize     int64  `yaml:"max_temp_size"`
	TempFileExpiry  string `yaml:"temp_file_expiry"`
	BandwidthBudget int64  `yaml:"bandwidth_budget"`

	// Failure backoff.
	FailureBackoff struct {
		Threshold int    `yaml:"threshold"`
		Window    string `yaml:"window"`
		Block     string `yaml:"block"`
	} `yaml:"failure_backoff"`
}

// loadConfig parses the plugin config from raw YAML bytes.
// Returns defaults if bytes are empty or nil.
func loadConfig(data []byte) TransferConfig {
	var cfg TransferConfig
	if len(data) > 0 {
		yaml.Unmarshal(data, &cfg)
	}
	// Apply defaults.
	if cfg.ReceiveMode == "" {
		cfg.ReceiveMode = "contacts"
	}
	return cfg
}

// reloadConfig implements the 6-field hot-reload with rollback logic.
// This replicates the configReloader.ReloadConfig() transfer section from cmd_daemon.go.
func (p *FileTransferPlugin) reloadConfig(newBytes []byte) {
	newCfg := loadConfig(newBytes)
	oldCfg := p.config

	ts := p.transferService
	if ts == nil {
		p.config = newCfg
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
		p.config = oldCfg
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
			ts.SetReceiveMode(p2pnet.ReceiveMode(newMode))
		}
		applied = append(applied, rollbackEntry{
			field:   "receive_mode",
			restore: func() { ts.SetReceiveMode(p2pnet.ReceiveMode(oldMode)) },
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

	// 6. Notify command.
	if oldCfg.NotifyCommand != newCfg.NotifyCommand {
		ts.SetNotifyCommand(newCfg.NotifyCommand)
		oldCmd := oldCfg.NotifyCommand
		applied = append(applied, rollbackEntry{
			field:   "notify_command",
			restore: func() { ts.SetNotifyCommand(oldCmd) },
		})
	}

	p.config = newCfg

	if len(applied) > 0 {
		fields := make([]string, len(applied))
		for i, a := range applied {
			fields[i] = a.field
		}
		slog.Info("plugin.config-reloaded", "plugin", "filetransfer", "changed", fmt.Sprintf("%v", fields))
	}
}
