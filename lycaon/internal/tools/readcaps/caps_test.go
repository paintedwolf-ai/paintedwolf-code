package readcaps

import "testing"

func TestLineLimitPositive(t *testing.T) {
	if LineLimit <= 0 {
		t.Fatalf("LineLimit = %d", LineLimit)
	}
}

func TestMaxMutationBytesBelowMaxFileBytes(t *testing.T) {
	if MaxMutationBytes >= MaxFileBytes {
		t.Fatalf("MaxMutationBytes (%d) must be strictly less than MaxFileBytes (%d)", MaxMutationBytes, MaxFileBytes)
	}
}
