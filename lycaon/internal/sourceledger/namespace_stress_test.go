//go:build stress

package sourceledger

import "testing"

func TestStressLargeDirectoryTransitionsDoNotWriteDescendants(t *testing.T) {
	testDirectoryTransitionWork(t, []int{1, 4096})
}
