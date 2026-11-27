//go:build darwin

package pythonnative

import "path/filepath"

// ReferenceWazeroUsesInterpreter reports true on macOS where hardened-runtime
// code signing forbids runtime-generated JIT code without JIT entitlements.
func ReferenceWazeroUsesInterpreter() bool { return true }

// UserDataDirs returns platform-appropriate user data search roots for the
// Portable Python image directory under home.
func UserDataDirs(home, channel string) []string {
	return []string{
		filepath.Join(home, ".local", "share", "paintedwolf-portable-python", "image"),
		filepath.Join(home, ".local", "share", channel, "portable-python", "image"),
		filepath.Join(home, "Library", "Application Support", channel, "portable-python", "image"),
	}
}
