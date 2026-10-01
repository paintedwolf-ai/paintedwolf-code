//go:build windows

package api

import "os"

// writeOwnerOnlyFile relies on the profile ACL and preserves the inode for readers.
func writeOwnerOnlyFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, daemonManifestMode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
