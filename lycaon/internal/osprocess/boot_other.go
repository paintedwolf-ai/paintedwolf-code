//go:build !darwin && !linux

package osprocess

// BootID has no source on this platform, so recorded identities stay unproven.
func BootID() (string, bool) { return "", false }
