//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
