//go:build windows

package auth

import "os"

// platformOwnership is a no-op on Windows (no Unix-style file ownership).
func platformOwnership(_ os.FileInfo) *fileOwnership {
	return nil
}
