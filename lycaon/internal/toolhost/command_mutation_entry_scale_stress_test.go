//go:build stress && (darwin || linux)

package toolhost

import "testing"

func TestStressComposedMutationEntryScale50000Files(t *testing.T) {
	runMutationEntryTree(t, 50000)
}
