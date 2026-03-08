//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// escalateToFileOwner re-execs the current command as the config file's owner
// when the current user lacks permission to read it. This handles the common
// case where the SSH login user differs from the service user (e.g. peerup vs shurli).
// On success, this function never returns (syscall.Exec replaces the process).
// On any failure, it returns silently and the normal error path handles it.
func escalateToFileOwner(configPath string, relayArgs []string) {
	f, err := os.Open(configPath)
	if err == nil {
		f.Close()
		return // readable, no escalation needed
	}
	if !os.IsPermission(err) {
		return
	}

	info, statErr := os.Stat(configPath)
	if statErr != nil {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}

	// Don't re-exec if we're already the file owner or root.
	uid := os.Getuid()
	if uid == int(stat.Uid) || uid == 0 {
		return
	}

	u, lookupErr := user.LookupId(strconv.Itoa(int(stat.Uid)))
	if lookupErr != nil {
		return
	}

	binary, exeErr := os.Executable()
	if exeErr != nil {
		return
	}

	sudoPath, pathErr := findExecutable("sudo")
	if pathErr != nil {
		return
	}

	// Re-exec: sudo -u <owner> <binary> relay <args...>
	execArgs := []string{"sudo", "-u", u.Username, binary, "relay"}
	execArgs = append(execArgs, relayArgs...)
	syscall.Exec(sudoPath, execArgs, os.Environ())
	// If exec fails, fall through to normal error handling.
}

// findExecutable locates an executable in PATH (like exec.LookPath but without
// importing os/exec just for this).
func findExecutable(name string) (string, error) {
	for _, dir := range strings.Split(os.Getenv("PATH"), ":") {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
