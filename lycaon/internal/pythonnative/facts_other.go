//go:build !darwin

package pythonnative

import "path/filepath"

// ReferenceWazeroUsesInterpreter reports false on non-macOS hosts where the
// reference wazero control backend uses the compiler.
func ReferenceWazeroUsesInterpreter() bool { return false }

// UserDataDirs returns platform-appropriate user data search roots for the
// Portable Python image directory under home.
func UserDataDirs(home, channel string) []string {
	return []string{
		filepath.Join(home, ".local", "share", "paintedwolf-portable-python", "image"),
		filepath.Join(home, ".local", "share", channel, "portable-python", "image"),
	}
}
