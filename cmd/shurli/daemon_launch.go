//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shurlinet/shurli/internal/config"
	"syscall"
)

const launchdLabel = "com.shurli.daemon"

// kickServiceDaemon tries to restart the daemon via the system service manager
// (launchd on macOS, systemd on Linux). Returns true if the service was kicked
// successfully. Returns false if no service is installed or the kick failed,
// signaling the caller to start the daemon directly.
func kickServiceDaemon() bool {
	switch runtime.GOOS {
	case "darwin":
		return kickLaunchd()
	case "linux":
		return kickSystemd()
	default:
		return false
	}
}

func kickLaunchd() bool {
	// Check if the plist is installed.
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	plist := home + "/Library/LaunchAgents/" + launchdLabel + ".plist"
	if _, err := os.Stat(plist); err != nil {
		return false
	}

	uid := strconv.Itoa(os.Getuid())
	cmd := exec.Command("launchctl", "kickstart", "-k", "gui/"+uid+"/"+launchdLabel)
	if err := cmd.Run(); err != nil {
		return false
	}
	fmt.Println("Daemon restarted via launchd.")
	fmt.Println("Logs: /tmp/shurli-daemon.log")
	return true
}

func kickSystemd() bool {
	// Check if the service unit exists.
	if _, err := os.Stat("/etc/systemd/system/shurli-daemon.service"); err != nil {
		return false
	}

	cmd := exec.Command("systemctl", "--user", "restart", "shurli-daemon")
	if err := cmd.Run(); err != nil {
		// Try system-level (might need sudo, but worth trying).
		cmd2 := exec.Command("sudo", "systemctl", "restart", "shurli-daemon")
		if err2 := cmd2.Run(); err2 != nil {
			return false
		}
	}
	fmt.Println("Daemon restarted via systemd.")
	fmt.Println("Logs: journalctl -u shurli-daemon -f")
	return true
}

// detachedProcAttr returns SysProcAttr that fully detaches the child process
// from the parent's terminal session (Unix: new session via Setsid).
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// promptInstallService asks the user whether to install shurli as a systemd service.
// Only offered on Linux when systemd is available. daemonPID is the background daemon
// process to kill before enabling the service (0 = no daemon running).
// reader/stdout are for interactive I/O. Returns true if service was installed.
func promptInstallService(reader *bufio.Reader, stdout io.Writer, daemonPID int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	// Check if systemd is available.
	if err := exec.Command("systemctl", "--version").Run(); err != nil {
		return false
	}
	// Check if already installed.
	if _, err := os.Stat("/etc/systemd/system/shurli-daemon.service"); err == nil {
		return false
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Install as system service (starts on boot)? [Y/n]")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return false
	}

	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}

	// Generate service file with current user and config-based memory limit.
	memoryLimit := "2G" // default
	if cfgPath, err := config.FindConfigFile(""); err == nil {
		if cfg, err := config.LoadHomeNodeConfig(cfgPath); err == nil && cfg.Network.MemoryLimit != "" {
			memoryLimit = cfg.Network.MemoryLimit
		}
	}
	serviceContent := generateServiceFile(user, memoryLimit)

	// Write to temp file, then sudo move into place.
	tmp, err := os.CreateTemp("", "shurli-daemon-*.service")
	if err != nil {
		fmt.Fprintf(stdout, "Failed to create temp file: %v\n", err)
		return false
	}
	tmpPath := tmp.Name()
	tmp.WriteString(serviceContent)
	tmp.Close()

	// Kill the background daemon before enabling the service.
	if daemonPID > 0 {
		if proc, err := os.FindProcess(daemonPID); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}

	if err := sudoRun("cp", tmpPath, "/etc/systemd/system/shurli-daemon.service"); err != nil {
		fmt.Fprintf(stdout, "Failed to install service file: %v\n", err)
		os.Remove(tmpPath)
		return false
	}
	os.Remove(tmpPath)

	if err := sudoRun("systemctl", "daemon-reload"); err != nil {
		fmt.Fprintf(stdout, "Failed to reload systemd: %v\n", err)
		return false
	}
	if err := sudoRun("systemctl", "enable", "--now", "shurli-daemon"); err != nil {
		fmt.Fprintf(stdout, "Failed to enable service: %v\n", err)
		return false
	}

	fmt.Fprintln(stdout, "Systemd service installed and started.")
	fmt.Fprintln(stdout, "  Logs:    journalctl -u shurli-daemon -f")
	fmt.Fprintln(stdout, "  Status:  systemctl status shurli-daemon")
	return true
}

// generateServiceFile returns a systemd unit file with the given user/group.
// ReadWritePaths is built dynamically from directories that actually exist.
// Uses the service user's home dir, not the current user's (they may differ
// when root runs shurli init for a dedicated service account).
func generateServiceFile(user, memoryLimit string) string {
	if memoryLimit == "" {
		memoryLimit = "2G"
	}
	rwPaths := "/run/user"
	if _, err := os.Stat("/etc/shurli"); err == nil {
		rwPaths = "/etc/shurli " + rwPaths
	}

	// Determine the service user's home directory. os.UserHomeDir() returns
	// the caller's home, which may be /root when installing for user "shurli".
	home := userHomeDir(user)
	if home != "" {
		for _, sub := range []string{".shurli", ".config/shurli"} {
			dir := filepath.Join(home, sub)
			if _, err := os.Stat(dir); err == nil {
				rwPaths = dir + " " + rwPaths
			}
		}
		dlDir := filepath.Join(home, "Downloads", "shurli")
		if _, err := os.Stat(dlDir); err == nil {
			rwPaths = dlDir + " " + rwPaths
		}
	}

	return fmt.Sprintf(`[Unit]
Description=Shurli daemon - P2P network service
Documentation=https://shurli.io
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/shurli daemon
Restart=on-failure
RestartSec=5
WatchdogSec=90
User=%s
Group=%s

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=%s
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

# Resource limits
LimitNOFILE=65536
MemoryMax=%s

[Install]
WantedBy=multi-user.target
`, user, user, rwPaths, memoryLimit)
}

// userHomeDir returns the home directory for the given username.
// Falls back to the current user's home if lookup fails.
func userHomeDir(username string) string {
	if u, err := user.Lookup(username); err == nil {
		return u.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}
