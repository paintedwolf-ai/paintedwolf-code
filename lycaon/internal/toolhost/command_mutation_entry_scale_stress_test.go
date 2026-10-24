//go:build stress && darwin

package toolhost

import (
	"testing"
	"time"
)

func TestComposedMutationEntryScale50000Files(t *testing.T) {
	copyElapsed, removeElapsed := runMutationEntryTree(t, 50000)
	if copyElapsed >= 60*time.Second {
		t.Errorf("cp exceeded required 60s: %s", copyElapsed)
	}
	if removeElapsed >= 10*time.Second {
		t.Errorf("rm exceeded required 10s: %s", removeElapsed)
	}
}
