//go:build !darwin

package fspath

import "path/filepath"

// canonicalizeExisting resolves the symlink chain.
func canonicalizeExisting(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	return resolved, err == nil
}
