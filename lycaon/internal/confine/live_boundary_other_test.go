//go:build !linux

package confine_test

import "testing"

// requireLiveBoundary returns the helper that applies the host's real boundary.
func requireLiveBoundary(t *testing.T) string {
	t.Helper()
	return requireSeatbelt(t)
}

// liveBoundaryRoots adds what a confined helper needs beside roots.
func liveBoundaryRoots(roots ...string) []string { return roots }
