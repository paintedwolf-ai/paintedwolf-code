package parsercorephase0

import (
	"testing"
)

// The tests below pin the C dynamic-precedence rule table from
// stack_node_add_link (stack.c:199-264) and stack_node_new (stack.c:137-176)
// against the persistent core, one case per C branch:
//
//	rule 1  same pair, higher subtree precedence: assign (can lower)
//	rule 2  same pair, not higher, or exact duplicate: no update
//	rule 3  mergeable predecessors: max with the INCOMING predecessor's
//	        stored value plus the payload contribution
//	rule 5  append: max with the appended link's contribution
//
// C keeps one stored running value per node. Rule 1 assignment can leave the
// stored value BELOW the maximum over the node's own links; every later
// operation folds from the stored value, never from a link recompute.

type ruleTableFixture struct {
	core       *Core
	rootP      NodeID
	rootQ      NodeID
	lowA       SubtreeID // shallow class X, production 1
	lowB       SubtreeID // shallow class X, production 2 (shallow-equal to lowA)
	high       SubtreeID // distinct shallow class
	top        SubtreeID // boundary payload, child-bearing
	checkpoint CheckpointID
}

func newRuleTableFixture(t *testing.T) ruleTableFixture {
	t.Helper()
	core := newTinyCoreWithLimits(t, Limits{MaxDerivations: 32, MaxPopPaths: 32})
	rootP, err := core.Seed(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	rootQ, err := core.Seed(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	lowA := appendShallowPayload(t, core, shallowPayloadSpec{
		symbol: 20, productionID: 1, startByte: 0, endByte: 10, childSymbols: []Symbol{40},
	})
	lowB := appendShallowPayload(t, core, shallowPayloadSpec{
		symbol: 20, productionID: 2, startByte: 0, endByte: 10, childSymbols: []Symbol{41},
	})
	high := appendShallowPayload(t, core, shallowPayloadSpec{
		symbol: 21, productionID: 3, startByte: 0, endByte: 10, childSymbols: []Symbol{42},
	})
	top := appendShallowPayload(t, core, shallowPayloadSpec{
		symbol: 30, startByte: 10, endByte: 11, childSymbols: []Symbol{50},
	})
	checkpoint := mustInternCheckpoint(t, core, make([]byte, 32))
	if err := core.SetPhaseCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	return ruleTableFixture{
		core: core, rootP: rootP.Node, rootQ: rootQ.Node,
		lowA: lowA, lowB: lowB, high: high, top: top, checkpoint: checkpoint,
	}
}

func (f ruleTableFixture) nodeMaximum(t *testing.T, id NodeID) int64 {
	t.Helper()
	record, err := f.core.node(id)
	if err != nil {
		t.Fatal(err)
	}
	return record.precedenceMax
}

// storedBelowLinkMaximum builds the rule-1 gadget: a node whose stored
// maximum (3) sits below the maximum over its own links (100). The gadget is
// the merge product of {P lowA 2, Q high 100} with an incoming same-pair
// replacement {P lowB 3}: C assigns 0+3 and never revisits the Q link.
func (f ruleTableFixture) storedBelowLinkMaximum(t *testing.T) NodeID {
	t.Helper()
	left, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowA, scoreDelta: 2},
		{prev: f.rootQ, payload: f.high, scoreDelta: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, left); got != 100 {
		t.Fatalf("fresh left maximum=%d, want 100", got)
	}
	right, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowB, scoreDelta: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	folded := precedenceMaximumWitness{}
	merged, changed, err := f.core.mergePredecessorsBounded(left, right, 0, &folded)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("gadget merge must replace the same-pair link")
	}
	if got := f.nodeMaximum(t, merged); got != 3 {
		t.Fatalf("gadget stored maximum=%d, want the rule-1 assignment 3", got)
	}
	return merged
}

// Rule 1 assignment must lower the stored value below the link maximum, and
// rule 2 exact duplicates must then leave both the value and the graph
// untouched: C performs zero mutation, so the merge must return the incumbent
// node without publishing a duplicate.
func TestPrecedenceRuleTableDuplicateMergeKeepsStoredValue(t *testing.T) {
	f := newRuleTableFixture(t)
	gadget := f.storedBelowLinkMaximum(t)
	gadgetRecord, err := f.core.node(gadget)
	if err != nil {
		t.Fatal(err)
	}
	var inline [inlineAdjacencyCapacity]linkRecord
	links, err := f.core.publishedNodeLinksInto(inline[:0], *gadgetRecord)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.core.appendAdjacencyNode(7, 10, links)
	if err != nil {
		t.Fatal(err)
	}
	nodesBefore := len(f.core.nodes)
	folded := precedenceMaximumWitness{}
	merged, changed, err := f.core.mergePredecessorsBounded(gadget, duplicate, 0, &folded)
	if err != nil {
		t.Fatal(err)
	}
	if changed || merged != gadget {
		t.Fatalf("duplicate merge changed=%v merged=%d, want unchanged incumbent %d", changed, merged, gadget)
	}
	if nodesAfter := len(f.core.nodes); nodesAfter != nodesBefore {
		t.Fatalf("duplicate merge published %d new nodes, want zero", nodesAfter-nodesBefore)
	}
	if got := f.nodeMaximum(t, gadget); got != 3 {
		t.Fatalf("stored maximum=%d after duplicate merge, want 3", got)
	}
}

// Rule 3 at the boundary reads the INCOMING predecessor's stored value. When
// the nested merge is a no-op and the incoming predecessor's stored value
// exceeds the boundary's, C still raises the boundary value. The incumbent
// here is the gadget (stored 3); the incoming twin stores 100 over an equal
// link set, so only the incoming read produces the update.
func TestPrecedenceRuleTableOuterMergeReadsIncomingPredecessor(t *testing.T) {
	f := newRuleTableFixture(t)
	gadget := f.storedBelowLinkMaximum(t)
	gadgetRecord, err := f.core.node(gadget)
	if err != nil {
		t.Fatal(err)
	}
	var inline [inlineAdjacencyCapacity]linkRecord
	links, err := f.core.publishedNodeLinksInto(inline[:0], *gadgetRecord)
	if err != nil {
		t.Fatal(err)
	}
	twin, err := f.core.appendAdjacencyNode(7, 10, links)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, twin); got != 100 {
		t.Fatalf("twin stored maximum=%d, want 100", got)
	}
	key := f.core.boundaryKey(8, 11)
	oldTop, err := f.core.condense(key, linkInput{prev: gadget, payload: f.top})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, oldTop.Node); got != 3 {
		t.Fatalf("boundary stored maximum=%d, want the gadget seed 3", got)
	}
	merged, err := f.core.condense(key, linkInput{prev: twin, payload: f.top})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, merged.Node); got != 100 {
		t.Fatalf("boundary maximum=%d after outer merge, want the incoming predecessor's 100", got)
	}
}

// Rule 3 on the changed path must also fold the incoming predecessor's
// PRE-MERGE stored value. The nested merge processes the incoming links in
// order {high assign 40, low assign 3}, so the merged node stores 3 while the
// incoming predecessor stores 40. C maxes the containing node with 40.
func TestPrecedenceRuleTableChangedMergeFoldsIncomingValue(t *testing.T) {
	f := newRuleTableFixture(t)
	lowD := appendShallowPayload(t, f.core, shallowPayloadSpec{
		symbol: 22, productionID: 4, startByte: 0, endByte: 10, childSymbols: []Symbol{43},
	})
	lowC := appendShallowPayload(t, f.core, shallowPayloadSpec{
		symbol: 22, productionID: 5, startByte: 0, endByte: 10, childSymbols: []Symbol{44},
	})
	inner, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowA, scoreDelta: 2},
		{prev: f.rootQ, payload: lowD, scoreDelta: 39},
	})
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootQ, payload: lowC, scoreDelta: 40},
		{prev: f.rootP, payload: f.lowB, scoreDelta: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, incoming); got != 40 {
		t.Fatalf("incoming stored maximum=%d, want 40", got)
	}
	outerLeft, err := f.core.appendAdjacencyNode(9, 10, []linkRecord{
		{prev: inner, payload: f.top, scoreDelta: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	outerRight, err := f.core.appendAdjacencyNode(9, 10, []linkRecord{
		{prev: incoming, payload: f.top, scoreDelta: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.nodeMaximum(t, outerLeft); got != 39 {
		t.Fatalf("outer left stored maximum=%d, want 39", got)
	}
	folded := precedenceMaximumWitness{}
	merged, changed, err := f.core.mergePredecessorsBounded(outerLeft, outerRight, 0, &folded)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("outer merge must rewrite the nested predecessor link")
	}
	if got := f.nodeMaximum(t, merged); got != 40 {
		t.Fatalf("outer maximum=%d, want the incoming pre-merge fold 40", got)
	}
}

// Rule 2 after a rule-1 assignment must record nothing. The incoming set
// first replaces the same-pair link (assign 3) and then presents an exact
// duplicate of the high sibling link. C leaves the assigned 3 in place.
func TestPrecedenceRuleTableDuplicateAfterReplacementRecordsNothing(t *testing.T) {
	f := newRuleTableFixture(t)
	left, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowA, scoreDelta: 2},
		{prev: f.rootQ, payload: f.high, scoreDelta: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowB, scoreDelta: 3},
		{prev: f.rootQ, payload: f.high, scoreDelta: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	folded := precedenceMaximumWitness{}
	merged, changed, err := f.core.mergePredecessorsBounded(left, right, 0, &folded)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("replacement merge must change the adjacency")
	}
	if got := f.nodeMaximum(t, merged); got != 3 {
		t.Fatalf("merged maximum=%d, want the rule-1 assignment 3", got)
	}
}

// Rule 5 appends must max the appended link's contribution into the stored
// value even though publication no longer recomputes over the adjacency.
func TestPrecedenceRuleTableAppendFoldsContribution(t *testing.T) {
	f := newRuleTableFixture(t)
	left, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootP, payload: f.lowA, scoreDelta: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := f.core.appendAdjacencyNode(7, 10, []linkRecord{
		{prev: f.rootQ, payload: f.high, scoreDelta: 77},
	})
	if err != nil {
		t.Fatal(err)
	}
	folded := precedenceMaximumWitness{}
	merged, changed, err := f.core.mergePredecessorsBounded(left, right, 0, &folded)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("append merge must change the adjacency")
	}
	if got := f.nodeMaximum(t, merged); got != 77 {
		t.Fatalf("merged maximum=%d, want the appended contribution 77", got)
	}
}
