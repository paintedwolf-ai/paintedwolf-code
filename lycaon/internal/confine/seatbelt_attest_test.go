//go:build darwin

package confine

import "testing"

// Unconfined processes fail positive membership attestation.
func TestAttestationDetectsUnconfined(t *testing.T) {
	// The test process has no applied profile.
	if member, _ := processSandboxed(); member {
		t.Fatal("an unsandboxed process must not report itself as sandboxed")
	}
	if err := attestConfined(); err == nil {
		t.Fatal("attestConfined must fail (detect not-sandboxed) in an unsandboxed process")
	}
}
