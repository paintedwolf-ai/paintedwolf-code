package gotreesitter

import "testing"

func TestRecoveredResultNodesAcyclicIsNonMutating(t *testing.T) {
	leaf := &Node{}
	left := &Node{children: []*Node{leaf}}
	right := &Node{children: []*Node{leaf}}
	root := &Node{children: []*Node{left, right}}
	if !recoveredResultNodesAcyclic([]*Node{root}) {
		t.Fatal("shared acyclic result graph rejected")
	}
	if len(root.children) != 2 || root.children[0] != left || root.children[1] != right || left.children[0] != leaf || right.children[0] != leaf {
		t.Fatal("validator mutated a clean result graph")
	}

	self := &Node{}
	self.children = []*Node{self}
	if recoveredResultNodesAcyclic([]*Node{self}) {
		t.Fatal("validator accepted self-cycle")
	}
	if len(self.children) != 1 || self.children[0] != self {
		t.Fatal("validator repaired the self-cycle instead of reporting it")
	}

	a := &Node{}
	b := &Node{}
	a.children = []*Node{b}
	b.children = []*Node{a}
	if recoveredResultNodesAcyclic([]*Node{a}) {
		t.Fatal("validator accepted ancestor back-edge")
	}
	if len(b.children) != 1 || b.children[0] != a {
		t.Fatal("validator repaired the ancestor back-edge instead of reporting it")
	}
}

func TestFlattenInvisibleRootChildrenHandlesCycles(t *testing.T) {
	meta := []SymbolMetadata{
		{Name: "root", Visible: true},
		{Name: "_hidden", Visible: false},
		{Name: "visible", Visible: true},
	}

	self := &Node{symbol: 1}
	self.children = []*Node{self}
	root := &Node{symbol: 0, children: []*Node{self}}
	flattenInvisibleRootChildren(root, nil, &Language{SymbolMetadata: meta})
	if len(root.children) != 0 {
		t.Fatalf("self-cycle flatten children = %d, want 0", len(root.children))
	}

	visible := &Node{symbol: 2}
	outer := &Node{symbol: 1}
	inner := &Node{symbol: 1}
	outer.children = []*Node{inner}
	inner.children = []*Node{outer, visible}
	root = &Node{symbol: 0, children: []*Node{outer}}
	flattenInvisibleRootChildren(root, nil, &Language{SymbolMetadata: meta})
	if len(root.children) != 1 || root.children[0] != visible {
		t.Fatalf("ancestor-cycle flatten children = %v, want visible leaf", root.children)
	}
}

func TestFlattenInvisibleRootChildrenStillFlattensCleanHiddenWrappers(t *testing.T) {
	meta := []SymbolMetadata{
		{Name: "root", Visible: true},
		{Name: "_hidden", Visible: false},
		{Name: "visible", Visible: true},
	}
	visible := &Node{symbol: 2}
	hidden := &Node{symbol: 1, children: []*Node{visible}}
	root := &Node{symbol: 0, children: []*Node{hidden}}
	flattenInvisibleRootChildren(root, nil, &Language{SymbolMetadata: meta})
	if len(root.children) != 1 || root.children[0] != visible {
		t.Fatalf("clean flatten children = %v, want visible leaf", root.children)
	}
}

// TestFlattenInvisibleRootChildrenPreservesSpanWhenHiddenLeafVanishes pins the
// wave-2 doxygen fix: when a trailing (or any) root child is an invisible,
// non-extra, childless (leaf) hidden symbol, appendFlattenedInvisibleRootChild
// has nothing to substitute for it — it is dropped from the children array
// entirely, with no sibling replacing its byte span. populateParentNode (via
// replaceNodeChildrenUnfielded) then recomputes root's span strictly from the
// SURVIVING children, which would otherwise silently shrink the root below
// the real content it structurally absorbed (doxygen's whole-block-comment
// ERROR fallback: a hidden `_text_line` leaf trailing an ERROR-wrapped hidden
// delimiter shrank the root from the full 67-byte comment down to just the
// first 3 bytes). flattenInvisibleRootChildren must widen back to the
// pre-flatten span afterward — a hidden node's bytes are still part of its
// parent's span in tree-sitter C even though the node itself never appears in
// the concrete tree.
func TestFlattenInvisibleRootChildrenPreservesSpanWhenHiddenLeafVanishes(t *testing.T) {
	meta := []SymbolMetadata{
		{Name: "root", Visible: true},
		{Name: "_hidden_delim", Visible: false},
		{Name: "wrapped", Visible: true},
		{Name: "_hidden_leaf", Visible: false},
	}
	wrapped := &Node{symbol: 2, startByte: 0, endByte: 3, endPoint: Point{Column: 3}}
	// A hidden LEAF trailing child: invisible, non-extra, zero children — the
	// exact shape appendFlattenedInvisibleRootChildWalk cannot substitute
	// anything for.
	hiddenLeaf := &Node{symbol: 3, startByte: 3, endByte: 10, startPoint: Point{Column: 3}, endPoint: Point{Column: 10}}
	root := &Node{symbol: 0, startByte: 0, endByte: 10, endPoint: Point{Column: 10}, children: []*Node{wrapped, hiddenLeaf}}

	flattenInvisibleRootChildren(root, nil, &Language{SymbolMetadata: meta})

	if len(root.children) != 1 || root.children[0] != wrapped {
		t.Fatalf("children = %v, want only the wrapped (visible) child", root.children)
	}
	if root.startByte != 0 || root.endByte != 10 {
		t.Fatalf("root span = [%d,%d), want [0,10) — span must not shrink when a hidden leaf child is dropped", root.startByte, root.endByte)
	}
	if root.endPoint.Column != 10 {
		t.Fatalf("root endPoint = %+v, want Column=10", root.endPoint)
	}
}
