//go:build stress && darwin

package toolhost

import "testing"

func TestComposedMutationEntryScale50000Files(t *testing.T) {
	runMutationEntryTree(t, 50000)
}
