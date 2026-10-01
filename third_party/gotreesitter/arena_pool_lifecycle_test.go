package gotreesitter

import "testing"

func TestOversizedFullArenaReleaseClearsAllDuplicateCheckoutSlots(t *testing.T) {
	DrainArenaPools()
	t.Cleanup(DrainArenaPools)

	arenaA := newNodeArena(arenaClassFull)
	arenaB := newNodeArena(arenaClassFull)
	unrelated := newNodeArena(arenaClassFull)
	backing := []*nodeArena{arenaB, arenaA, unrelated}
	fullArenaPool.mu.Lock()
	fullArenaPool.free = backing[:2]
	fullArenaPool.mu.Unlock()

	checkedOutA := acquireNodeArena(arenaClassFull)
	checkedOutB := acquireNodeArena(arenaClassFull)
	if checkedOutA != arenaA || checkedOutB != arenaB {
		t.Fatal("test setup did not preserve LIFO checkout order")
	}
	t.Cleanup(func() {
		if checkedOutB.refs.Load() > 0 {
			checkedOutB.Release()
		}
	})

	checkedOutA.Release()
	checkedOutA = acquireNodeArena(arenaClassFull)
	if checkedOutA != arenaA {
		t.Fatal("repooling did not return arena A on the next checkout")
	}

	fullArenaPool.mu.Lock()
	before := fullArenaPool.free[:cap(fullArenaPool.free)]
	duplicateA := before[0] == arenaA && before[1] == arenaA
	unrelatedBefore := before[2]
	fullArenaPool.mu.Unlock()
	if !duplicateA {
		t.Fatal("test setup did not create duplicate inactive references to arena A")
	}
	if unrelatedBefore != unrelated {
		t.Fatal("test setup lost the unrelated inactive slot")
	}

	checkedOutA.allocatedBytes = maxRetainedFullArenaBytes + 1
	checkedOutA.Release()

	fullArenaPool.mu.Lock()
	after := fullArenaPool.free[:cap(fullArenaPool.free)]
	firstA, secondA, unrelatedAfter := after[0], after[1], after[2]
	fullArenaPool.mu.Unlock()
	if firstA != nil || secondA != nil {
		t.Fatal("oversized rejected arena remains reachable through duplicate inactive slots")
	}
	if unrelatedAfter != unrelated {
		t.Fatal("discard cleared an unrelated inactive slot")
	}
}
