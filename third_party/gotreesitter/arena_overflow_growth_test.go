package gotreesitter

import (
	"testing"
	"unsafe"
)

// TestArenaOverflowSlabGrowthBounded guards the asm.js-class arena-exhaustion
// fix: overflow node slabs previously doubled without bound, so a single large
// flat-tree parse (box2d.js / lua_binarytrees.js) allocated a final backing slab
// as large as everything before it combined. Up to ~half of that last slab was
// never used, and because the per-parse memory budget is charged against slab
// *capacity* (allocatedBytes), that speculative tail tripped the 512 MB budget
// hundreds of MB before the live tree actually reached it.
//
// The invariant: no overflow slab may exceed maxOverflowSlabGrowthBytes, and
// total node capacity waste (allocated slots minus live slots) stays within one
// ceiling-sized slab regardless of how many nodes are allocated.
func TestArenaOverflowSlabGrowthBounded(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	nodeSize := int(unsafe.Sizeof(Node{}))
	ceiling := maxOverflowSlabGrowthBytes / nodeSize

	// Allocate well into the linear-growth regime: past the primary slab and
	// several ceiling-sized overflow slabs.
	primary := len(arena.nodes)
	total := primary + ceiling*8
	for i := 0; i < total; i++ {
		_ = arena.allocNode()
	}

	if len(arena.nodeSlabs) == 0 {
		t.Fatal("expected overflow slabs after allocating past primary capacity")
	}

	// No single overflow slab may exceed the growth ceiling.
	maxSlab := 0
	capacity := primary
	for i := range arena.nodeSlabs {
		c := len(arena.nodeSlabs[i].data)
		capacity += c
		if c > maxSlab {
			maxSlab = c
		}
		if c > ceiling {
			t.Fatalf("overflow slab %d capacity=%d exceeds growth ceiling=%d (unbounded doubling regressed)", i, c, ceiling)
		}
	}

	// Capacity waste must be bounded by a single ceiling slab. Unbounded doubling
	// produced waste of up to ~50% of total capacity; the bound is one slab.
	waste := capacity - arena.used
	if waste > ceiling {
		t.Fatalf("node capacity waste=%d nodes (%d MB) exceeds one ceiling slab=%d nodes; "+
			"unbounded overflow doubling has regressed",
			waste, int64(waste)*int64(nodeSize)/(1<<20), ceiling)
	}

	// The reported allocatedBytes (what the memory budget is charged) must match
	// the true slab capacity, and waste beyond live usage must stay minimal.
	if got, want := arena.allocatedBytes, arena.nodeStructBytesAllocated()+arena.childSliceBytesAllocated()+arena.fieldIDBytesAllocated()+arena.fieldSourceBytesAllocated(); got != want {
		t.Fatalf("allocatedBytes=%d, recomputed node+slice bytes=%d", got, want)
	}
}

// TestBoundedNextSlabCapCapsGrowth checks the growth helper directly across the
// geometric-then-linear transition for the Node element size.
func TestBoundedNextSlabCapCapsGrowth(t *testing.T) {
	nodeSize := int(unsafe.Sizeof(Node{}))
	ceiling := maxOverflowSlabGrowthBytes / nodeSize

	// Geometric regime: doubles while below the ceiling.
	if got := boundedNextSlabCap(1000, minArenaNodeCap, nodeSize); got != 2000 {
		t.Fatalf("boundedNextSlabCap(1000) = %d, want 2000 (doubling below ceiling)", got)
	}
	// Transition: doubling past the ceiling is clamped to the ceiling.
	if got := boundedNextSlabCap(ceiling*3/4, minArenaNodeCap, nodeSize); got != ceiling {
		t.Fatalf("boundedNextSlabCap(ceiling*3/4) = %d, want ceiling=%d", got, ceiling)
	}
	// Linear regime: once at/above the ceiling, stays flat at the ceiling.
	if got := boundedNextSlabCap(ceiling, minArenaNodeCap, nodeSize); got != ceiling {
		t.Fatalf("boundedNextSlabCap(ceiling) = %d, want ceiling=%d (flat linear growth)", got, ceiling)
	}
	// A single request larger than the ceiling is still satisfied (variable-length
	// rawShapeChild ranges rely on this).
	big := ceiling + 5000
	if got := boundedNextSlabCap(ceiling, big, nodeSize); got != big {
		t.Fatalf("boundedNextSlabCap(minReq=%d) = %d, want %d", big, got, big)
	}
}

// TestArenaCompactFullLeafOverflowBounded extends the overflow-growth guard to
// one of the compact-leaf allocators (allocCompactFullLeaf). Review A flagged
// the compact-leaf overflow slabs as still doubling without bound after the
// node-slab fix, so leaf-heavy parses (leaf-dense flat trees) still carried
// ~50% capacity overshoot in the final doubled slab — the same budget-tripping
// tail the node fix removed. The invariant mirrors the node path: no compact
// leaf overflow slab exceeds maxOverflowSlabGrowthBytes, and total leaf-slab
// capacity waste stays within one ceiling-sized slab regardless of how many
// leaves are allocated.
func TestArenaCompactFullLeafOverflowBounded(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	elemSize := int(unsafe.Sizeof(compactFullLeaf{}))
	ceiling := maxOverflowSlabGrowthBytes / elemSize

	// Allocate the primary slab plus several ceiling-sized overflow slabs so the
	// growth is well into the linear regime.
	primary := defaultCompactFullLeafSlabCap(arenaClassFull)
	total := primary + ceiling*5
	for i := 0; i < total; i++ {
		if arena.allocCompactFullLeaf() == nil {
			t.Fatal("allocCompactFullLeaf returned nil")
		}
	}
	if len(arena.compactFullLeafSlabs) < 2 {
		t.Fatalf("expected compact-full-leaf overflow slabs, got %d slab(s)", len(arena.compactFullLeafSlabs))
	}

	capacity := 0
	for i := range arena.compactFullLeafSlabs {
		c := len(arena.compactFullLeafSlabs[i].data)
		capacity += c
		if c > ceiling {
			t.Fatalf("compact-full-leaf overflow slab %d capacity=%d exceeds growth ceiling=%d (unbounded doubling regressed)", i, c, ceiling)
		}
	}

	// Every slab but the last is full (we allocated past them), so waste is
	// bounded by the last, at-most-ceiling-sized slab. Unbounded doubling grew
	// the final slab as large as everything before it, blowing past this bound.
	waste := capacity - total
	if waste > ceiling {
		t.Fatalf("compact-full-leaf capacity waste=%d leaves (%d MB) exceeds one ceiling slab=%d; "+
			"unbounded overflow doubling has regressed",
			waste, int64(waste)*int64(elemSize)/(1<<20), ceiling)
	}
}
