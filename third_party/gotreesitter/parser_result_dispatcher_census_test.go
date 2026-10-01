package gotreesitter

import "testing"

func TestDispatcherArmCensusFingerprintDetectsEveryObservableChange(t *testing.T) {
	build := func() (*Node, *Node) {
		arena := newNodeArena(arenaClassFull)
		leaf1 := newLeafNodeInArena(arena, 10, true, 0, 3, Point{Column: 0}, Point{Column: 3})
		leaf2 := newLeafNodeInArena(arena, 11, true, 3, 6, Point{Column: 3}, Point{Column: 6})
		root := newParentNodeInArena(arena, 1, true, []*Node{leaf1, leaf2}, []FieldID{0, 0}, 0)
		return root, leaf2
	}

	root, _ := build()
	before := captureDispatcherFingerprint(root)
	if len(before) != 3 {
		t.Fatalf("captureDispatcherFingerprint visited %d nodes, want 3 (root + 2 leaves)", len(before))
	}

	// No-op: capturing twice without any mutation must show no rewrite.
	if visited, rewritten := diffDispatcherFingerprint(before, captureDispatcherFingerprint(root)); rewritten != 0 || visited == 0 {
		t.Fatalf("identical snapshots reported visited=%d rewritten=%d, want visited>0 rewritten=0", visited, rewritten)
	}

	// Span edit: the most common shape of a result-compatibility rewrite
	// (extending or trimming a node's byte range).
	root2, leaf2b := build()
	before2 := captureDispatcherFingerprint(root2)
	leaf2b.endByte = 9
	if visited, rewritten := diffDispatcherFingerprint(before2, captureDispatcherFingerprint(root2)); rewritten == 0 {
		t.Fatalf("span edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	// Point edit: a normalizer can repair row and column positions while the
	// byte range stays unchanged.
	rootPoint, leafPoint := build()
	beforePoint := captureDispatcherFingerprint(rootPoint)
	leafPoint.endPoint = Point{Row: 2, Column: 1}
	if visited, rewritten := diffDispatcherFingerprint(beforePoint, captureDispatcherFingerprint(rootPoint)); rewritten == 0 {
		t.Fatalf("point edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	// Parser-state edits are public and affect later incremental reuse.
	rootParseState, leafParseState := build()
	beforeParseState := captureDispatcherFingerprint(rootParseState)
	leafParseState.parseState = 41
	if visited, rewritten := diffDispatcherFingerprint(beforeParseState, captureDispatcherFingerprint(rootParseState)); rewritten == 0 {
		t.Fatalf("parse-state edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	rootPreGotoState, leafPreGotoState := build()
	beforePreGotoState := captureDispatcherFingerprint(rootPreGotoState)
	leafPreGotoState.preGotoState = 42
	if visited, rewritten := diffDispatcherFingerprint(beforePreGotoState, captureDispatcherFingerprint(rootPreGotoState)); rewritten == 0 {
		t.Fatalf("pre-goto-state edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	// Flag edit: some normalizers mark a node MISSING/EXTRA without touching
	// its span at all.
	root3, leaf2c := build()
	before3 := captureDispatcherFingerprint(root3)
	leaf2c.setFlag(nodeFlagExtra, true)
	if visited, rewritten := diffDispatcherFingerprint(before3, captureDispatcherFingerprint(root3)); rewritten == 0 {
		t.Fatalf("flag edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	rootDirty, leafDirty := build()
	beforeDirty := captureDispatcherFingerprint(rootDirty)
	leafDirty.setDirty(true)
	if visited, rewritten := diffDispatcherFingerprint(beforeDirty, captureDispatcherFingerprint(rootDirty)); rewritten == 0 {
		t.Fatalf("dirty-flag edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	// A field edit can change only the field that a child fills.
	root4, _ := build()
	before4 := captureDispatcherFingerprint(root4)
	root4.setFieldIDs([]FieldID{0, 7})
	if visited, rewritten := diffDispatcherFingerprint(before4, captureDispatcherFingerprint(root4)); rewritten == 0 {
		t.Fatalf("field edit went undetected: visited=%d rewritten=%d", visited, rewritten)
	}

	// Structural edit: some normalizers insert a synthesized/recovered node.
	root5, _ := build()
	before5 := captureDispatcherFingerprint(root5)
	arena := newNodeArena(arenaClassFull)
	extra := newLeafNodeInArena(arena, 12, true, 6, 9, Point{Column: 6}, Point{Column: 9})
	root5.children = append(root5.children, extra)
	after5 := captureDispatcherFingerprint(root5)
	if len(after5) != len(before5)+1 {
		t.Fatalf("captureDispatcherFingerprint after insertion visited %d nodes, want %d", len(after5), len(before5)+1)
	}
	if visited, rewritten := diffDispatcherFingerprint(before5, after5); rewritten == 0 {
		t.Fatalf("structural insertion went undetected: visited=%d rewritten=%d", visited, rewritten)
	}
}

func TestDispatcherArmCensusFingerprintPreservesCompactFinalChildRefs(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	arena.finalChildRefs = true
	leaf := newCompactFullLeafInArena(arena, 2, true, 0, 4, Point{}, Point{Column: 4})
	leaf.parseState = 12
	leaf.preGotoState = 11
	parent := newPendingParentInArena(arena, 1, true, 0, []stackEntry{
		newStackEntryCompactFullLeaf(leaf.parseState, leaf),
	}, 0, 4, Point{}, Point{Column: 4}, false)
	parent.parseState = 14
	parent.preGotoState = 13
	entry := newStackEntryPendingParent(parent.parseState, parent)
	root := materializeStackEntryPendingParent(arena, &entry, pendingParentMaterializeForFinalTree)
	if root == nil || !nodeHasFinalChildRefs(root) {
		t.Fatal("fixture did not retain compact final-child refs")
	}

	lazy := captureDispatcherFingerprint(root)
	if got := arena.finalChildRefsMaterializedParents; got != 0 {
		t.Fatalf("fingerprint materialized final-child parent ranges = %d, want 0", got)
	}
	if got := arena.finalChildRefsSingleChildAccesses; got != 0 {
		t.Fatalf("fingerprint accessed final-child refs through materialization = %d, want 0", got)
	}
	if got := arena.finalChildRefsSingleChildMaterializedChildren; got != 0 {
		t.Fatalf("fingerprint materialized compact children = %d, want 0", got)
	}
	if !nodeHasFinalChildRefs(root) {
		t.Fatal("fingerprint drained final-child refs")
	}

	if child := root.Child(0); child == nil {
		t.Fatal("explicit materialization returned nil child")
	}
	eager := captureDispatcherFingerprint(root)
	if visited, rewritten := diffDispatcherFingerprint(lazy, eager); rewritten != 0 || visited == 0 {
		t.Fatalf("lazy and materialized fingerprints differ: visited=%d rewritten=%d", visited, rewritten)
	}
}

func TestDispatcherCensusRequiresExactOne(t *testing.T) {
	for _, value := range []string{"", "0", "false", "2"} {
		t.Setenv("GTS_DISPATCHER_CENSUS", value)
		if dispatcherCensusEnabled() {
			t.Fatalf("GTS_DISPATCHER_CENSUS=%q enabled the census", value)
		}
	}
	t.Setenv("GTS_DISPATCHER_CENSUS", "1")
	if !dispatcherCensusEnabled() {
		t.Fatal("GTS_DISPATCHER_CENSUS=1 did not enable the census")
	}
}

func TestDispatcherArmSubpassCensusRecordsDistinctReceipts(t *testing.T) {
	t.Setenv("GTS_DISPATCHER_CENSUS", "1")

	arena := newNodeArena(arenaClassFull)
	left := newLeafNodeInArena(arena, 10, true, 0, 3, Point{}, Point{Column: 3})
	right := newLeafNodeInArena(arena, 11, true, 3, 6, Point{Column: 3}, Point{Column: 6})
	root := newParentNodeInArena(arena, 1, true, []*Node{left, right}, nil, 0)
	parser := &Parser{}
	ctx := resultCompatibilityContext{root: root, parser: parser}

	dispatcherArmSubpassCensus(ctx, "dispatch.fixture", func(census materializationSubpassCensus) {
		census.run("dispatch.fixture.retype-left", func() {
			left.symbol = 12
		})
		census.run("dispatch.fixture.inspect-right", func() {})
	})

	want := map[string]normalizationNamedPassStats{
		"dispatch.fixture": {
			name:           "dispatch.fixture",
			checked:        1,
			run:            1,
			nodesVisited:   3,
			nodesRewritten: 1,
		},
		"dispatch.fixture.retype-left": {
			name:           "dispatch.fixture.retype-left",
			checked:        1,
			run:            1,
			nodesVisited:   3,
			nodesRewritten: 1,
		},
		"dispatch.fixture.inspect-right": {
			name:         "dispatch.fixture.inspect-right",
			checked:      1,
			run:          1,
			nodesVisited: 3,
		},
	}
	if got := len(parser.normalizationStats.namedPasses); got != len(want) {
		t.Fatalf("named census receipts = %d, want %d: %+v", got, len(want), parser.normalizationStats.namedPasses)
	}
	for _, got := range parser.normalizationStats.namedPasses {
		expected, ok := want[got.name]
		if !ok {
			t.Errorf("unexpected named census receipt %+v", got)
			continue
		}
		if got != expected {
			t.Errorf("%s census receipt = %+v, want %+v", got.name, got, expected)
		}
	}
}

func TestDispatcherArmSubpassCensusDisabledRunsWithoutReceipts(t *testing.T) {
	t.Setenv("GTS_DISPATCHER_CENSUS", "")

	arena := newNodeArena(arenaClassFull)
	leaf := newLeafNodeInArena(arena, 10, true, 0, 3, Point{}, Point{Column: 3})
	root := newParentNodeInArena(arena, 1, true, []*Node{leaf}, nil, 0)
	parser := &Parser{}
	ctx := resultCompatibilityContext{root: root, parser: parser}

	dispatcherArmSubpassCensus(ctx, "dispatch.fixture", func(census materializationSubpassCensus) {
		census.run("dispatch.fixture.retype", func() {
			leaf.symbol = 12
		})
	})

	if leaf.symbol != 12 {
		t.Fatalf("disabled census did not run the subpass: symbol = %d", leaf.symbol)
	}
	if got := parser.normalizationStats.namedPasses; len(got) != 0 {
		t.Fatalf("disabled census recorded receipts: %+v", got)
	}
}
