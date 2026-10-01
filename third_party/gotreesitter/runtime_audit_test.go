package gotreesitter

import "testing"

func TestRuntimeAuditObserveEntriesSkipsNoTreePayload(t *testing.T) {
	leaf := &Node{symbol: 1}
	audit := &runtimeAudit{
		currentTokenGen: 1,
		nodeInfo: map[*Node]runtimeAuditNodeInfo{
			leaf: {gen: 1, kind: runtimeAuditNodeKindLeaf},
		},
		seenNode: make(map[*Node]struct{}),
	}
	entries := []stackEntry{
		newStackEntryNode(1, leaf),
		newStackEntryNoTreeNode(2, &noTreeNode{}),
	}

	var parents, leaves, childSlices, childPointers uint64
	audit.observeEntries(entries, &parents, &leaves, &childSlices, &childPointers)

	if leaves != 1 {
		t.Fatalf("leaf retained = %d, want 1", leaves)
	}
	if parents != 0 {
		t.Fatalf("parent retained = %d, want 0", parents)
	}
	if childSlices != 0 {
		t.Fatalf("child slices retained = %d, want 0", childSlices)
	}
	if childPointers != 0 {
		t.Fatalf("child pointers retained = %d, want 0", childPointers)
	}
}

func TestRuntimeAuditMergeHeaderCountersByEquivState(t *testing.T) {
	audit := &runtimeAudit{equivEnabled: true}

	audit.recordMergeHeaderEq()
	audit.recordMergeDeepResult(true, false)

	if got := audit.mergeHeaderEqTotal; got != 1 {
		t.Fatalf("merge header eq total = %d, want 1", got)
	}
	if got := audit.mergeDeepFalse; got != 1 {
		t.Fatalf("merge deep false = %d, want 1", got)
	}
	if got := audit.mergeHeaderDeepDivergent; got != 1 {
		t.Fatalf("merge header deep divergent = %d, want 1", got)
	}
	if got := len(audit.equivStateStats()); got != 0 {
		t.Fatalf("equiv state stats without state = %d, want 0", got)
	}

	audit.setEquivState(StateID(42))
	audit.recordMergeHeaderEq()
	audit.recordMergeDeepResult(true, false)
	audit.recordMergeDeepResult(false, true)
	audit.clearEquivState()

	if got := audit.mergeHeaderEqTotal; got != 2 {
		t.Fatalf("merge header eq total after state = %d, want 2", got)
	}
	if got := audit.mergeDeepTrue; got != 1 {
		t.Fatalf("merge deep true = %d, want 1", got)
	}
	if got := audit.mergeDeepFalse; got != 2 {
		t.Fatalf("merge deep false after state = %d, want 2", got)
	}
	if got := audit.mergeHeaderDeepDivergent; got != 2 {
		t.Fatalf("merge header deep divergent after state = %d, want 2", got)
	}

	stats := audit.equivStateStats()
	if len(stats) != 1 {
		t.Fatalf("equiv state stats = %d, want 1", len(stats))
	}
	stat := stats[0]
	if stat.State != StateID(42) {
		t.Fatalf("equiv state = %d, want 42", stat.State)
	}
	if stat.MergeHeaderEqTotal != 1 {
		t.Fatalf("state merge header eq total = %d, want 1", stat.MergeHeaderEqTotal)
	}
	if stat.MergeDeepTrue != 1 {
		t.Fatalf("state merge deep true = %d, want 1", stat.MergeDeepTrue)
	}
	if stat.MergeDeepFalse != 1 {
		t.Fatalf("state merge deep false = %d, want 1", stat.MergeDeepFalse)
	}
	if stat.MergeHeaderDeepDivergent != 1 {
		t.Fatalf("state merge header deep divergent = %d, want 1", stat.MergeHeaderDeepDivergent)
	}
}

func TestRuntimeAuditChildPayloadSurvivors(t *testing.T) {
	child := &Node{symbol: 1}
	retainedParent := &Node{symbol: 2, children: []*Node{child, child}}
	droppedParent := &Node{symbol: 3, children: []*Node{child}}
	audit := &runtimeAudit{
		enabled:         true,
		currentTokenGen: 1,
		tokenActive:     true,
		nodeInfo:        make(map[*Node]runtimeAuditNodeInfo),
		seenNode:        make(map[*Node]struct{}),
	}
	audit.recordNodeAlloc(retainedParent, runtimeAuditNodeKindParent)
	audit.recordNodeAlloc(droppedParent, runtimeAuditNodeKindParent)

	var parents, leaves, childSlices, childPointers uint64
	audit.observeNode(retainedParent, &parents, &leaves, &childSlices, &childPointers)
	audit.currentParentRetained = parents
	audit.currentLeafRetained = leaves
	audit.currentChildSlicesRetained = childSlices
	audit.currentChildPointersRetained = childPointers
	audit.finishToken()

	if got := audit.totalParentAllocated; got != 2 {
		t.Fatalf("parent allocated = %d, want 2", got)
	}
	if got := audit.totalParentRetained; got != 1 {
		t.Fatalf("parent retained = %d, want 1", got)
	}
	if got := audit.totalParentDropped; got != 1 {
		t.Fatalf("parent dropped = %d, want 1", got)
	}
	if got := audit.totalChildSlicesAllocated; got != 2 {
		t.Fatalf("child slices allocated = %d, want 2", got)
	}
	if got := audit.totalChildSlicesRetained; got != 1 {
		t.Fatalf("child slices retained = %d, want 1", got)
	}
	if got := audit.totalChildSlicesDropped; got != 1 {
		t.Fatalf("child slices dropped = %d, want 1", got)
	}
	if got := audit.totalChildPointersAllocated; got != 3 {
		t.Fatalf("child pointers allocated = %d, want 3", got)
	}
	if got := audit.totalChildPointersRetained; got != 2 {
		t.Fatalf("child pointers retained = %d, want 2", got)
	}
	if got := audit.totalChildPointersDropped; got != 1 {
		t.Fatalf("child pointers dropped = %d, want 1", got)
	}
}

func TestRuntimeAuditReduceChildPathSurvivors(t *testing.T) {
	child := &Node{symbol: 1}
	retainedParent := &Node{symbol: 2, children: []*Node{child, child}}
	droppedParent := &Node{symbol: 3, children: []*Node{child, child, child}}
	audit := &runtimeAudit{
		enabled:         true,
		currentTokenGen: 1,
		tokenActive:     true,
		nodeInfo:        make(map[*Node]runtimeAuditNodeInfo),
		seenNode:        make(map[*Node]struct{}),
	}
	audit.recordNodeAlloc(retainedParent, runtimeAuditNodeKindParent)
	audit.recordReduceParentChildPath(retainedParent, reduceChildPathAllVisible, len(retainedParent.children))
	audit.recordNodeAlloc(droppedParent, runtimeAuditNodeKindParent)
	audit.recordReduceParentChildPath(droppedParent, reduceChildPathAllVisible, len(droppedParent.children))

	var parents, leaves, childSlices, childPointers uint64
	audit.observeNode(retainedParent, &parents, &leaves, &childSlices, &childPointers)
	audit.currentParentRetained = parents
	audit.currentLeafRetained = leaves
	audit.currentChildSlicesRetained = childSlices
	audit.currentChildPointersRetained = childPointers
	audit.finishToken()

	got := audit.reduceChildPathRuntime(reduceChildPathAllVisible)
	if got.SlicesAllocated != 2 {
		t.Fatalf("path slices allocated = %d, want 2", got.SlicesAllocated)
	}
	if got.SlicesRetained != 1 {
		t.Fatalf("path slices retained = %d, want 1", got.SlicesRetained)
	}
	if got.SlicesDropped != 1 {
		t.Fatalf("path slices dropped = %d, want 1", got.SlicesDropped)
	}
	if got.PointersAllocated != 5 {
		t.Fatalf("path pointers allocated = %d, want 5", got.PointersAllocated)
	}
	if got.PointersRetained != 2 {
		t.Fatalf("path pointers retained = %d, want 2", got.PointersRetained)
	}
	if got.PointersDropped != 3 {
		t.Fatalf("path pointers dropped = %d, want 3", got.PointersDropped)
	}
}
