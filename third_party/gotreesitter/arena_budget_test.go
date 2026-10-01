package gotreesitter

import "testing"

func TestArenaMemoryBudgetExhaustedByNodeSlabGrowth(t *testing.T) {
	arena := newNodeArena(arenaClassIncremental)
	arena.setBudget(1)

	for i := 0; i < len(arena.nodes); i++ {
		_ = arena.allocNode()
	}
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted before overflow node slab allocation")
	}

	_ = arena.allocNode()
	if !arena.budgetExhausted() {
		t.Fatal("budget not exhausted after overflow node slab allocation")
	}
}

func TestArenaMemoryBudgetExhaustedByChildSlabGrowth(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	base := defaultChildSliceCap(arena.class)
	arena.setBudget(1)

	_ = arena.allocNodeSlice(base)
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted before child slab overflow")
	}

	_ = arena.allocNodeSlice(base)
	if !arena.budgetExhausted() {
		t.Fatal("budget not exhausted after child slab overflow")
	}
}

func TestArenaMemoryBudgetExhaustedByFieldSlabGrowth(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	base := defaultFieldSliceCap(arena.class)
	arena.setBudget(1)

	_ = arena.allocFieldIDSlice(base)
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted before field slab overflow")
	}

	_ = arena.allocFieldIDSlice(base)
	if !arena.budgetExhausted() {
		t.Fatal("budget not exhausted after field slab overflow")
	}
}

func TestArenaMemoryBudgetExhaustedByFieldSourceSlabGrowth(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	base := defaultFieldSliceCap(arena.class)
	arena.setBudget(1)

	_ = arena.allocFieldSourceSlice(base)
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted before field-source slab overflow")
	}

	_ = arena.allocFieldSourceSlice(base)
	if !arena.budgetExhausted() {
		t.Fatal("budget not exhausted after field-source slab overflow")
	}
}

func TestArenaMemoryBudgetUsesPerParseGrowth(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	arena.allocatedBytes = 96 << 20

	arena.setBudget(64 << 20)
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted by retained arena baseline")
	}

	arena.allocatedBytes += 63 << 20
	if arena.budgetExhausted() {
		t.Fatal("budget exhausted before per-parse growth reached budget")
	}

	arena.allocatedBytes += 1 << 20
	if !arena.budgetExhausted() {
		t.Fatal("budget not exhausted after per-parse growth reached budget")
	}
}

func TestArenaResetRecomputesAllocatedBytes(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	baseChild := defaultChildSliceCap(arena.class)
	baseField := defaultFieldSliceCap(arena.class)

	for i := 0; i < len(arena.nodes)+1; i++ {
		_ = arena.allocNode()
	}
	_ = arena.allocNodeSlice(baseChild)
	_ = arena.allocNodeSlice(baseChild)
	_ = arena.allocFieldIDSlice(baseField)
	_ = arena.allocFieldIDSlice(baseField)
	_ = arena.allocFieldSourceSlice(baseField)
	_ = arena.allocFieldSourceSlice(baseField)

	if arena.allocatedBytes <= 0 {
		t.Fatal("allocatedBytes should be positive before reset")
	}

	arena.reset()

	var want int64
	want += nodeBytesForCap(len(arena.nodes))
	for _, slab := range arena.nodeSlabs {
		want += nodeBytesForCap(len(slab.data))
	}
	for _, slab := range arena.childSlabs {
		want += childSliceBytesForCap(len(slab.data))
	}
	for _, slab := range arena.fieldSlabs {
		want += fieldSliceBytesForCap(len(slab.data))
	}
	for _, slab := range arena.fieldSourceSlabs {
		want += fieldSourceSliceBytesForCap(len(slab.data))
	}
	if arena.allocatedBytes != want {
		t.Fatalf("allocatedBytes after reset = %d, want %d", arena.allocatedBytes, want)
	}
}
