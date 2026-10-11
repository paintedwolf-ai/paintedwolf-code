//go:build stress

package sourceledger

import "testing"

func TestLargeDirectoryTransitionsDoNotWriteDescendants(t *testing.T) {
	testDirectoryTransitionWork(t, []int{1, 4096})
}
