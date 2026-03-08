//go:build windows

package main

import "syscall"

func kickServiceDaemon() bool {
	return false
}

func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
