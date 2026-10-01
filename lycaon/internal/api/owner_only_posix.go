//go:build !windows

package api

import "os"

// writeOwnerOnlyFile creates a fresh 0600 file before writing any bytes.
// Exclusive creation rejects a path or symlink created after removal.
func writeOwnerOnlyFile(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, daemonManifestMode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
