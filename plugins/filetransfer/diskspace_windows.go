//go:build windows

package filetransfer

import (
	"fmt"
	"syscall"
	"unsafe"
)

// checkDiskSpace verifies that the receive directory has enough free space.
func (ts *TransferService) checkDiskSpace(needed int64) error {
	return checkDiskSpaceAt(ts.receiveDir, needed)
}

// checkDiskSpaceAt verifies that the given directory has enough free space.
func checkDiskSpaceAt(dir string, needed int64) error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytesAvailable uint64
	dirPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	ret, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		0,
		0,
	)
	if ret == 0 {
		return fmt.Errorf("GetDiskFreeSpaceEx: %w", callErr)
	}

	available := int64(freeBytesAvailable)
	// Require at least 10% headroom above needed.
	required := needed + needed/10
	if available < required {
		return fmt.Errorf("insufficient disk space: need %d bytes, have %d", required, available)
	}
	return nil
}
