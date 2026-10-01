package toolcontract_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestMutatesContent(t *testing.T) {
	for _, name := range []string{"write", "edit", "replace_lines"} {
		if !toolcontract.MutatesContent(name) {
			t.Fatalf("%q should be a file mutation tool", name)
		}
	}
	for _, name := range []string{"read", "grep", "command", "verify", ""} {
		if toolcontract.MutatesContent(name) {
			t.Fatalf("%q should not be a file mutation tool", name)
		}
	}
}
