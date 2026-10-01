//go:build windows

package configdir

// restrictConfigDir relies on the profile directory ACL.
func restrictConfigDir(string) error { return nil }
