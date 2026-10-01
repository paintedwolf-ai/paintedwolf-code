// Package fsname folds filesystem-level respellings that resolve to the same on-disk file.
package fsname

import "strings"

// CanonicalBasename strips NTFS alternate data streams and trailing spaces or dots.
func CanonicalBasename(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return strings.TrimRight(name, ". ")
}

// EqualBasename reports whether candidate names the same protected file as
// target once respellings are folded. target must already be canonical.
func EqualBasename(candidate, target string) bool {
	return strings.EqualFold(CanonicalBasename(candidate), target)
}
