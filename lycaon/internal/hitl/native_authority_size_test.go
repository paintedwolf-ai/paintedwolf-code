package hitl

import (
	"math"
	"testing"
)

func TestNativeAuthorityStorageSizeRefusesUnsignedOverflow(t *testing.T) {
	for _, size := range []uintptr{0, 8, ^uintptr(0)} {
		got, err := authorityStorageBytes(size)
		if uint64(size) > math.MaxInt64 {
			if err == nil || got != 0 {
				t.Fatalf("oversized authority allocation admitted signed bytes %d: %v", got, err)
			}
		} else if err != nil || got < 0 || uint64(got) != uint64(size) {
			t.Fatalf("representable authority allocation changed size: %d -> %d: %v", size, got, err)
		}
	}
}
