package gotreesitter

import "testing"

var compactPackedGSSReceiptAuditKinds = []string{
	"node",
	"no-tree",
	"compact-checkpoint",
	"compact-full",
	"pending",
	"transient",
}

type compactPackedGSSReceiptAuditFixture struct {
	arena     *nodeArena
	language  *Language
	parser    *Parser
	transient *transientParentScratch
	scratch   *glrMergeScratch
}

func newCompactPackedGSSReceiptAuditFixture() *compactPackedGSSReceiptAuditFixture {
	arena := newNodeArena(arenaClassFull)
	language := &Language{CompactPackedGSSVersionOrderCertified: true}
	transient := &transientParentScratch{}
	parser := &Parser{
		language: language,
		reduceScratch: &reduceBuildScratch{
			transientParents: transient,
		},
	}
	fixture := &compactPackedGSSReceiptAuditFixture{
		arena:     arena,
		language:  language,
		parser:    parser,
		transient: transient,
		scratch: &glrMergeScratch{
			language:                    language,
			packedGSSVersionOrderActive: true,
			arena:                       arena,
			parser:                      parser,
		},
	}
	parser.mergeScratch = fixture.scratch
	return fixture
}

func (f *compactPackedGSSReceiptAuditFixture) rawReceipt(symbol Symbol, childCount int, start uint32, seed Symbol) rawShapeRef {
	children := make([]stackEntry, childCount)
	for i := range children {
		child := newLeafNodeInArena(
			f.arena,
			seed+Symbol(i),
			true,
			start,
			start+1,
			Point{},
			Point{Column: 1},
		)
		children[i] = newStackEntryNode(1, child)
	}
	return f.parser.captureRawShape(nil, f.arena, symbol, 0, children, 0, len(children))
}

func (f *compactPackedGSSReceiptAuditFixture) entry(kind string, physicalSymbol, rawSymbol Symbol, rawChildCount int, start, end uint32, seed Symbol) stackEntry {
	ref := f.rawReceipt(rawSymbol, rawChildCount, start, seed)
	switch kind {
	case "node":
		node := newLeafNodeInArena(f.arena, physicalSymbol, true, start, end, Point{}, Point{Column: end - start})
		node.rawShape = ref
		return newStackEntryNode(7, node)
	case "no-tree":
		node := newNoTreeLeafNodeInArena(f.arena, physicalSymbol, true, start, end, Point{}, Point{Column: end - start})
		node.rawShape = ref
		return newStackEntryNoTreeNode(7, node)
	case "compact-checkpoint":
		leaf := newCompactCheckpointLeafInArena(f.arena, physicalSymbol, true, start, end, externalScannerCheckpointRef{})
		leaf.rawShape = ref
		return newStackEntryCompactCheckpointLeaf(7, leaf)
	case "compact-full":
		leaf := newCompactFullLeafInArena(f.arena, physicalSymbol, true, start, end, Point{}, Point{Column: end - start})
		leaf.rawShape = ref
		return newStackEntryCompactFullLeaf(7, leaf)
	case "pending":
		var children []stackEntry
		if rawChildCount > 0 {
			child := newLeafNodeInArena(f.arena, seed+100, true, start, end, Point{}, Point{Column: end - start})
			children = []stackEntry{newStackEntryNode(1, child)}
		}
		parent := newPendingParentInArena(f.arena, physicalSymbol, true, 0, children, start, end, Point{}, Point{Column: end - start}, false)
		parent.rawShape = ref
		return newStackEntryPendingParent(7, parent)
	case "transient":
		var children []*Node
		if rawChildCount > 0 {
			child := newLeafNodeInArena(f.arena, seed+100, true, start, end, Point{}, Point{Column: end - start})
			children = []*Node{child}
		}
		parent := f.transient.allocParent(f.arena, physicalSymbol, true, children, 0, false)
		if len(children) == 0 {
			parent.startByte = start
			parent.endByte = end
		}
		parent.rawShape = ref
		return newStackEntryNode(7, parent)
	default:
		panic("unknown compact packed-GSS receipt audit kind: " + kind)
	}
}

func compactPackedGSSReceiptAuditEquivalent(f *compactPackedGSSReceiptAuditFixture, left, right stackEntry) bool {
	return cStackLinkPayloadsEquivalentAtOffsets(f.scratch, left, right, 5, true, 5, true)
}

func TestCompactPackedGSSReceiptAuditRawHeaderTruthTable(t *testing.T) {
	for kindIndex, kind := range compactPackedGSSReceiptAuditKinds {
		t.Run(kind, func(t *testing.T) {
			f := newCompactPackedGSSReceiptAuditFixture()
			physical := Symbol(100 + kindIndex)
			left := f.entry(kind, physical, 70, 1, 10, 12, 1)
			equivalent := f.entry(kind, physical+20, 70, 1, 10, 12, 10)
			if !compactPackedGSSReceiptAuditEquivalent(f, left, equivalent) {
				t.Fatal("equal raw headers were rejected because Go payload details differ")
			}

			differentSymbol := f.entry(kind, physical, 71, 1, 10, 12, 20)
			if compactPackedGSSReceiptAuditEquivalent(f, left, differentSymbol) {
				t.Fatal("different raw symbols compared equal")
			}

			differentArity := f.entry(kind, physical, 70, 2, 10, 12, 30)
			if compactPackedGSSReceiptAuditEquivalent(f, left, differentArity) {
				t.Fatal("different raw child counts compared equal")
			}
		})
	}
}

func TestCompactPackedGSSReceiptAuditCrossRepresentationPairs(t *testing.T) {
	f := newCompactPackedGSSReceiptAuditFixture()
	entries := make([]stackEntry, len(compactPackedGSSReceiptAuditKinds))
	for i, kind := range compactPackedGSSReceiptAuditKinds {
		entries[i] = f.entry(kind, Symbol(200+i), 80, 1, 10, 12, Symbol(40+i))
		if !rawShapeRefIsArenaBacked(stackEntryRawShapeRef(entries[i])) {
			t.Fatalf("%s did not carry an arena-backed receipt", kind)
		}
	}
	for leftIndex, leftKind := range compactPackedGSSReceiptAuditKinds {
		for rightIndex, rightKind := range compactPackedGSSReceiptAuditKinds {
			if leftIndex == rightIndex {
				continue
			}
			t.Run(leftKind+"-to-"+rightKind, func(t *testing.T) {
				if !compactPackedGSSReceiptAuditEquivalent(f, entries[leftIndex], entries[rightIndex]) {
					t.Fatal("equal C headers depended on the Go payload representation")
				}
			})
		}
	}
}

func TestCompactPackedGSSReceiptAuditRejectsCrossArenaBackedRefsInBothOrders(t *testing.T) {
	for kindIndex, kind := range compactPackedGSSReceiptAuditKinds {
		t.Run(kind, func(t *testing.T) {
			current := newCompactPackedGSSReceiptAuditFixture()
			foreign := newCompactPackedGSSReceiptAuditFixture()
			physical := Symbol(300 + kindIndex)
			currentEntry := current.entry(kind, physical, 90, 1, 10, 12, 1)
			foreignEntry := foreign.entry(kind, physical, 90, 1, 10, 12, 1)
			currentRef := stackEntryRawShapeRef(currentEntry)
			foreignRef := stackEntryRawShapeRef(foreignEntry)
			if currentRef != foreignRef || !rawShapeRefIsArenaBacked(currentRef) {
				t.Fatalf("raw refs = (%d, %d), want an arena-relative collision", currentRef, foreignRef)
			}
			if compactPackedGSSReceiptAuditEquivalent(current, foreignEntry, currentEntry) {
				t.Fatal("foreign left payload was accepted")
			}
			if compactPackedGSSReceiptAuditEquivalent(current, currentEntry, foreignEntry) {
				t.Fatal("foreign right payload was accepted")
			}
		})
	}
}

func TestCompactPackedGSSReceiptAuditRejectsForeignZeroChildSentinels(t *testing.T) {
	for kindIndex, kind := range compactPackedGSSReceiptAuditKinds {
		t.Run(kind, func(t *testing.T) {
			current := newCompactPackedGSSReceiptAuditFixture()
			foreign := newCompactPackedGSSReceiptAuditFixture()
			physical := Symbol(400 + kindIndex)
			currentEntry := current.entry(kind, physical, physical, 0, 10, 10, 1)
			currentPeer := current.entry(kind, physical, physical, 0, 10, 10, 2)
			foreignEntry := foreign.entry(kind, physical, physical, 0, 10, 10, 3)
			for label, entry := range map[string]stackEntry{
				"current": currentEntry,
				"peer":    currentPeer,
				"foreign": foreignEntry,
			} {
				if got := stackEntryRawShapeRef(entry); got != rawShapeZeroChildRef {
					t.Fatalf("%s raw ref = %d, want zero-child sentinel", label, got)
				}
			}
			if !compactPackedGSSReceiptAuditEquivalent(current, currentEntry, currentPeer) {
				t.Fatal("current zero-child payloads were rejected")
			}
			if compactPackedGSSReceiptAuditEquivalent(current, foreignEntry, currentEntry) {
				t.Error("foreign zero-child left payload was accepted")
			}
			if compactPackedGSSReceiptAuditEquivalent(current, currentEntry, foreignEntry) {
				t.Error("foreign zero-child right payload was accepted")
			}
		})
	}
}

func TestCompactPackedGSSReceiptAuditRejectsStaleSlabSlots(t *testing.T) {
	kinds := []string{"no-tree", "compact-checkpoint", "compact-full", "pending"}
	for kindIndex, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			f := newCompactPackedGSSReceiptAuditFixture()
			physical := Symbol(500 + kindIndex)
			left := f.entry(kind, physical, 100, 1, 10, 12, 1)
			right := f.entry(kind, physical, 100, 1, 10, 12, 2)
			if !compactPackedGSSReceiptAuditEquivalent(f, left, right) {
				t.Fatal("active slab payloads were rejected")
			}
			switch kind {
			case "no-tree":
				for i := range f.arena.noTreeNodeSlabs {
					f.arena.noTreeNodeSlabs[i].used = 0
				}
			case "compact-checkpoint":
				for i := range f.arena.compactCheckpointLeafSlabs {
					f.arena.compactCheckpointLeafSlabs[i].used = 0
				}
			case "compact-full":
				for i := range f.arena.compactFullLeafSlabs {
					f.arena.compactFullLeafSlabs[i].used = 0
				}
			case "pending":
				for i := range f.arena.pendingParentSlabs {
					f.arena.pendingParentSlabs[i].used = 0
				}
			}
			if compactPackedGSSReceiptAuditEquivalent(f, left, right) {
				t.Fatal("inactive slab payloads were accepted")
			}
		})
	}
}

func TestCompactPackedGSSReceiptAuditRejectsForeignActiveTransient(t *testing.T) {
	f := newCompactPackedGSSReceiptAuditFixture()
	foreignTransient := &transientParentScratch{}
	makeEntry := func(transient *transientParentScratch, seed Symbol) stackEntry {
		child := newLeafNodeInArena(f.arena, seed, true, 10, 12, Point{}, Point{Column: 2})
		parent := transient.allocParent(f.arena, 110, true, []*Node{child}, 0, false)
		parent.rawShape = f.rawReceipt(110, 1, 10, seed+20)
		return newStackEntryNode(7, parent)
	}
	current := makeEntry(f.transient, 1)
	currentPeer := makeEntry(f.transient, 2)
	foreign := makeEntry(foreignTransient, 3)
	if !compactPackedGSSReceiptAuditEquivalent(f, current, currentPeer) {
		t.Fatal("current active transient parents were rejected")
	}
	if compactPackedGSSReceiptAuditEquivalent(f, foreign, current) {
		t.Fatal("foreign active transient left parent was accepted")
	}
	if compactPackedGSSReceiptAuditEquivalent(f, current, foreign) {
		t.Fatal("foreign active transient right parent was accepted")
	}
}

func TestCompactPackedGSSReceiptAuditZeroArityPendingAndTransient(t *testing.T) {
	for _, kind := range []string{"pending", "transient"} {
		t.Run(kind, func(t *testing.T) {
			f := newCompactPackedGSSReceiptAuditFixture()
			left := f.entry(kind, 120, 120, 0, 10, 10, 1)
			right := f.entry(kind, 120, 120, 0, 10, 10, 2)
			if got := stackEntryRawShapeRef(left); got != rawShapeZeroChildRef {
				t.Fatalf("left raw ref = %d, want zero-child sentinel", got)
			}
			if got := stackEntryRawShapeRef(right); got != rawShapeZeroChildRef {
				t.Fatalf("right raw ref = %d, want zero-child sentinel", got)
			}
			if !compactPackedGSSReceiptAuditEquivalent(f, left, right) {
				t.Fatal("equal zero-arity reduction headers were rejected")
			}
		})
	}
}
