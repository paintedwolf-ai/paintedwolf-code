package gotreesitter

import "testing"

func TestGSSPreflightDenseReachMarks(t *testing.T) {
	var owner gssScratch
	root := owner.allocNode(stackEntry{state: 1}, nil, 1)
	head := owner.allocNode(stackEntry{state: 2}, root, 2)
	other := owner.allocNode(stackEntry{state: 3}, nil, 1)
	merge := &glrMergeScratch{gssOwner: &owner}
	p := acquirePreflightForScratch(merge)

	if got := p.canReach(head, other); got {
		t.Fatal("unrelated GSS nodes reported reachable")
	}
	if p.reachSeen != nil {
		t.Fatal("dense GSS reach walk allocated fallback visited map")
	}
	if mark, ok := owner.reachMarkFor(head, &p.reachSlabHint); !ok || *mark != p.reachGeneration {
		t.Fatalf("head mark = %v, %v; want current generation", mark, ok)
	}

	previousWalk := p.reachGeneration
	if got := p.canReach(head, other); got {
		t.Fatal("cached unrelated GSS nodes reported reachable")
	}
	if p.reachGeneration != previousWalk {
		t.Fatal("cached reach result started a new walk")
	}
	if got := p.canReach(head, root); !got {
		t.Fatal("dense GSS reachable chain rejected after another walk")
	}
	if p.reachSeen != nil {
		t.Fatal("dense GSS reach path allocated fallback visited map")
	}

	oldCacheGeneration := p.reachCacheGeneration
	p.resetReachCacheGeneration()
	if p.reachCacheGeneration == oldCacheGeneration || len(p.reachCache) != 0 {
		t.Fatalf("reach cache reset = generation %d, len %d; want new generation and empty view", p.reachCacheGeneration, len(p.reachCache))
	}
}

func TestGSSPreflightDenseReachMarksAfterSlabGrowth(t *testing.T) {
	var owner gssScratch
	owner.slabs = []gssNodeSlab{{data: make([]gssNode, 1)}}
	owner.slabCursor = 0
	owner.recomputeAllocatedBytes()
	root := owner.allocNode(stackEntry{state: 1}, nil, 1)
	merge := &glrMergeScratch{gssOwner: &owner}
	p := acquirePreflightForScratch(merge)

	// Allocate a second slab after mark provisioning. The first reach walk must
	// provision marks for the new slab instead of using the pointer-map fallback.
	head := owner.allocNode(stackEntry{state: 2}, root, 2)
	if got := p.canReach(head, root); !got {
		t.Fatal("slab-growth dense reach rejected a reachable chain")
	}
	if p.reachSeen != nil {
		t.Fatal("slab growth allocated the visited map instead of dense marks")
	}
	if mark, ok := owner.reachMarkFor(head, &p.reachSlabHint); !ok || *mark != p.reachGeneration {
		t.Fatalf("slab-growth mark = %v, %v; want current generation", mark, ok)
	}
}
