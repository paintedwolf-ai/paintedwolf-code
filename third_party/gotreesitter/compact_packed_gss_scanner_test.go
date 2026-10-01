package gotreesitter

import "testing"

func TestCertifiedCStackLinkCompactFullScannerStates(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	stateful := newC26lCheckpointScanner()
	language := &Language{CompactPackedGSSVersionOrderCertified: true, ExternalScanner: stateful}
	if !arena.setExternalScannerCheckpointIdentityForLanguage(language) {
		t.Fatal("arena rejected external-scanner identity")
	}
	scratch := &glrMergeScratch{
		language:                    language,
		packedGSSVersionOrderActive: true,
		arena:                       arena,
		parser:                      &Parser{language: language},
	}

	compact := func(startState, endState byte, checkpoint bool) stackEntry {
		leaf := newCompactFullLeafInArena(arena, 60, true, 30, 32, Point{}, Point{Column: 2})
		leaf.rawShape = rawShapeZeroChildRef
		leaf.setExternalScannerToken(true)
		if checkpoint {
			leaf.checkpoint = arena.recordExternalScannerCompactCheckpoint([]byte{startState}, []byte{endState})
			leaf.hasCheckpoint = true
		}
		return newStackEntryCompactFullLeaf(9, leaf)
	}
	node := func(startState, endState byte) stackEntry {
		leaf := newLeafNodeInArena(arena, 60, true, 30, 32, Point{}, Point{Column: 2})
		leaf.rawShape = rawShapeZeroChildRef
		leaf.setExternalScannerToken(true)
		if !arena.recordExternalScannerLeafCheckpoint(leaf, []byte{startState}, []byte{endState}) {
			t.Fatal("record Node scanner checkpoint")
		}
		return newStackEntryNode(9, leaf)
	}

	compactA := compact(1, 2, true)
	compactSame := compact(9, 2, true)
	compactDifferent := compact(1, 3, true)
	compactAbsent := compact(1, 2, false)
	if !cStackLinkPayloadsEquivalentAtOffsets(scratch, compactA, compactSame, 30, true, 30, true) {
		t.Fatal("certified C comparison used a compact leaf scanner start state")
	}
	if cStackLinkPayloadsEquivalentAtOffsets(scratch, compactA, compactDifferent, 30, true, 30, true) {
		t.Fatal("certified C comparison ignored a compact leaf scanner end-state mismatch")
	}
	if cStackLinkPayloadsEquivalentAtOffsets(scratch, compactA, compactAbsent, 30, true, 30, true) {
		t.Fatal("certified C comparison accepted a compact leaf without a checkpoint")
	}
	if !cStackLinkPayloadsEquivalentAtOffsets(scratch, node(7, 2), compactA, 30, true, 30, true) {
		t.Fatal("certified C comparison rejected equal Node and compact leaf scanner states")
	}

	mismatchedScanner := newC26lCheckpointScanner()
	mismatchedScanner.grammarID = []byte("different-grammar")
	mismatchedLanguage := &Language{CompactPackedGSSVersionOrderCertified: true, ExternalScanner: mismatchedScanner}
	mismatchedScratch := *scratch
	mismatchedScratch.language = mismatchedLanguage
	mismatchedScratch.parser = &Parser{language: mismatchedLanguage}
	if cStackLinkPayloadsEquivalentAtOffsets(&mismatchedScratch, compactA, compactSame, 30, true, 30, true) {
		t.Fatal("certified C comparison accepted an identity-mismatched compact checkpoint")
	}
}

func TestCertifiedCStackLinkStatelessScannerAcrossLeafRepresentations(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	language := &Language{CompactPackedGSSVersionOrderCertified: true, ExternalScanner: &capabilityExternalScanner{}}
	scratch := &glrMergeScratch{
		language:                    language,
		packedGSSVersionOrderActive: true,
		arena:                       arena,
		parser:                      &Parser{language: language},
	}

	node := newLeafNodeInArena(arena, 61, true, 30, 32, Point{}, Point{Column: 2})
	node.rawShape = rawShapeZeroChildRef
	node.setExternalScannerToken(true)
	noTree := newNoTreeLeafNodeInArena(arena, 61, true, 30, 32, Point{}, Point{Column: 2})
	noTree.rawShape = rawShapeZeroChildRef
	noTree.setExternalScannerToken(true)
	checkpoint := newCompactCheckpointLeafInArena(arena, 61, true, 30, 32, externalScannerCheckpointRef{})
	checkpoint.rawShape = rawShapeZeroChildRef
	checkpoint.setExternalScannerToken(true)
	compact := newCompactFullLeafInArena(arena, 61, true, 30, 32, Point{}, Point{Column: 2})
	compact.rawShape = rawShapeZeroChildRef
	compact.setExternalScannerToken(true)

	left := newStackEntryNode(9, node)
	cases := map[string]stackEntry{
		"no-tree":            newStackEntryNoTreeNode(9, noTree),
		"compact-checkpoint": newStackEntryCompactCheckpointLeaf(9, checkpoint),
		"compact-full":       newStackEntryCompactFullLeaf(9, compact),
	}
	for name, right := range cases {
		t.Run(name, func(t *testing.T) {
			if !cStackLinkPayloadsEquivalentAtOffsets(scratch, left, right, 30, true, 30, true) {
				t.Fatal("certified C comparison rejected equal stateless scanner leaves")
			}
		})
	}
}

func TestCertifiedCStackLinkRejectsMalformedScannerCheckpointRanges(t *testing.T) {
	arena := newNodeArena(arenaClassFull)
	stateful := newC26lCheckpointScanner()
	language := &Language{CompactPackedGSSVersionOrderCertified: true, ExternalScanner: stateful}
	if !arena.setExternalScannerCheckpointIdentityForLanguage(language) {
		t.Fatal("arena rejected external-scanner identity")
	}
	scratch := &glrMergeScratch{
		language:                    language,
		packedGSSVersionOrderActive: true,
		arena:                       arena,
		parser:                      &Parser{language: language},
	}
	valid := arena.recordExternalScannerCompactCheckpoint([]byte{1}, []byte{2})
	makeCompact := func(checkpoint externalScannerCheckpointRef) stackEntry {
		leaf := newCompactFullLeafInArena(arena, 60, true, 30, 32, Point{}, Point{Column: 2})
		leaf.rawShape = rawShapeZeroChildRef
		leaf.setExternalScannerToken(true)
		leaf.checkpoint = checkpoint
		leaf.hasCheckpoint = true
		return newStackEntryCompactFullLeaf(9, leaf)
	}

	used := arena.fieldSourceSlabs[valid.end.slab].used
	if used >= len(arena.fieldSourceSlabs[valid.end.slab].data) {
		t.Fatal("test needs unused scanner snapshot capacity")
	}
	badEnd := valid
	badEnd.end.off = uint32(used)
	badStart := valid
	badStart.start.off = uint32(used)
	validEntry := makeCompact(valid)
	for name, checkpoint := range map[string]externalScannerCheckpointRef{
		"end beyond used":   badEnd,
		"start beyond used": badStart,
	} {
		t.Run(name, func(t *testing.T) {
			if cStackLinkPayloadsEquivalentAtOffsets(scratch, validEntry, makeCompact(checkpoint), 30, true, 30, true) {
				t.Fatal("certified C comparison accepted a malformed scanner checkpoint range")
			}
		})
	}
}
