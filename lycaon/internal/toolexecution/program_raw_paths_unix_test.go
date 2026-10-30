//go:build linux

package toolexecution

import "testing"

// The selected OS admits these distinct byte spellings. The actual native read
// door and final review observer must see the same bytes; JSON replacement is
// neither a path conversion nor evidence of a successful read.
func TestNativeEntryPreservesRawFilesystemOperandBytes(t *testing.T) {
	for name, path := range map[string]string{"ff": "input\xff", "fe": "input\xfe", "replacement_scalar": "input\ufffd"} {
		t.Run(name, func(t *testing.T) { nativeReadAdmissionFixture(t, path) })
	}
}
