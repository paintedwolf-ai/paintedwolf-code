//go:build linux

package confine_test

import "testing"

// requireLiveBoundary returns the helper that applies the host's real boundary,
// failing where Linux confinement should exist and does not.
func requireLiveBoundary(t *testing.T) string {
	t.Helper()
	return helperBinary(t, requireLinuxSandbox(t))
}

// liveBoundaryRoots adds what a confined helper needs beside roots.
func liveBoundaryRoots(roots ...string) []string { return sandboxRoots(roots...) }
