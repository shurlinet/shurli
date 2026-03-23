//go:build windows

package main

import (
	"bufio"
	"io"
	"syscall"
)

func kickServiceDaemon() bool {
	return false
}

func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func promptInstallService(_ *bufio.Reader, _ io.Writer, _ int) bool {
	return false
}
