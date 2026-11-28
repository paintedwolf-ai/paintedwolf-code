package hoststate

import (
	"errors"
	"testing"
)

// Overlapping holds share one memo store generation, repeated release of one
// hold is idempotent and cannot prematurely drop another active hold, final
// release clears the store, and a subsequent hold allocates a fresh generation.
func TestHoldAuthorityMemosStoreGenerationLifecycle(t *testing.T) {
	if _, err := heldAuthorityMemos(); !errors.Is(err, ErrAuthorityMemosNotHeld) {
		t.Fatalf("heldAuthorityMemos before hold = %v, want ErrAuthorityMemosNotHeld", err)
	}

	releaseA := HoldAuthorityMemos()
	defer releaseA()

	authorityMemos.mu.Lock()
	gen1 := authorityMemos.generation
	store1 := authorityMemos.store
	holds1 := authorityMemos.holds
	authorityMemos.mu.Unlock()
	if store1 == nil || holds1 != 1 {
		t.Fatalf("after first hold: store=%v holds=%d, want non-nil and 1", store1, holds1)
	}

	// Overlapping second hold shares the exact same store and generation.
	releaseB := HoldAuthorityMemos()
	defer releaseB()

	authorityMemos.mu.Lock()
	genOverlap := authorityMemos.generation
	storeOverlap := authorityMemos.store
	holdsOverlap := authorityMemos.holds
	authorityMemos.mu.Unlock()
	if genOverlap != gen1 || storeOverlap != store1 || holdsOverlap != 2 {
		t.Fatalf("overlapping hold: gen=%d/%d sameStore=%t holds=%d, want same gen/store and holds=2",
			genOverlap, gen1, storeOverlap == store1, holdsOverlap)
	}

	// Repeated release of A must be idempotent and leave B's hold intact.
	releaseA()
	releaseA()

	gotStore, err := heldAuthorityMemos()
	if err != nil {
		t.Fatalf("heldAuthorityMemos while B still held: %v", err)
	}
	authorityMemos.mu.Lock()
	holdsAfterA := authorityMemos.holds
	authorityMemos.mu.Unlock()
	if gotStore != store1 || holdsAfterA != 1 {
		t.Fatalf("after double releaseA: sameStore=%t holds=%d, want true and 1", gotStore == store1, holdsAfterA)
	}

	// Releasing B drops the store completely.
	releaseB()
	releaseB()

	if _, err := heldAuthorityMemos(); !errors.Is(err, ErrAuthorityMemosNotHeld) {
		t.Fatalf("heldAuthorityMemos after final release = %v, want ErrAuthorityMemosNotHeld", err)
	}
	authorityMemos.mu.Lock()
	storeCleared := authorityMemos.store
	holdsCleared := authorityMemos.holds
	authorityMemos.mu.Unlock()
	if storeCleared != nil || holdsCleared != 0 {
		t.Fatalf("after final release: store=%v holds=%d, want nil and 0", storeCleared, holdsCleared)
	}

	// A subsequent hold starts a fresh generation with a new store instance.
	releaseC := HoldAuthorityMemos()
	defer releaseC()

	authorityMemos.mu.Lock()
	gen2 := authorityMemos.generation
	store2 := authorityMemos.store
	holds2 := authorityMemos.holds
	authorityMemos.mu.Unlock()
	if gen2 != gen1+1 || store2 == nil || store2 == store1 || holds2 != 1 {
		t.Fatalf("fresh hold: gen=%d (want %d) newStore=%t holds=%d",
			gen2, gen1+1, store2 != nil && store2 != store1, holds2)
	}
}
