package gotreesitter_test

import "github.com/odvcencio/gotreesitter"

// --- Wave7/plight measurement-spike prototype -----------------------------
//
// soaTree is a struct-of-arrays mirror of a materialized *gotreesitter.Node
// tree, built by a pure POST-PARSE converter (public API only, zero parser
// changes). It exists solely to measure the deltas an index-based node store
// would plausibly change: bytes/node, GC-scan cost while retained, and
// full-walk throughput. It is NOT a candidate replacement tree
// implementation: no mutation, no incremental-edit support, and (deliberately)
// no cursor/query integration -- see the pointer-light measurement report for
// why query integration against this shape is out of scope for a "just
// enough to measure" prototype.
//
// Layout: parent/child references are uint32 indices into the flat,
// preorder-numbered arrays below (a CSR-style child list: node i's children
// are childIdx[firstChild[i] : firstChild[i]+childCount[i]]). There is no
// pointer anywhere in this struct except the slice headers themselves --
// no []*Node, no *parent, nothing for the GC's pointer-scan pass to chase
// per element.
const soaNoParent = ^uint32(0)

const (
	soaFlagNamed uint8 = 1 << iota
	soaFlagMissing
	soaFlagExtra
	soaFlagHasError
)

type soaTree struct {
	nodeCount int

	symbol     []gotreesitter.Symbol
	startByte  []uint32
	endByte    []uint32
	parent     []uint32
	firstChild []uint32
	childCount []uint16
	flags      []uint8
	childIdx   []uint32
}

// buildSoATree converts an already-materialized *Node tree into the SoA form
// above. Two linear passes over the tree via the public Node API (ChildCount
// / Child), both using explicit stacks rather than recursion so pathologically
// deep trees can't blow the Go call stack.
func buildSoATree(root *gotreesitter.Node) *soaTree {
	if root == nil {
		return &soaTree{}
	}

	// Pass 1: count nodes and edges.
	n := 0
	edges := 0
	countStack := make([]*gotreesitter.Node, 0, 256)
	countStack = append(countStack, root)
	for len(countStack) > 0 {
		last := len(countStack) - 1
		node := countStack[last]
		countStack = countStack[:last]
		n++
		cc := node.ChildCount()
		edges += cc
		for i := 0; i < cc; i++ {
			countStack = append(countStack, node.Child(i))
		}
	}

	t := &soaTree{
		nodeCount:  n,
		symbol:     make([]gotreesitter.Symbol, n),
		startByte:  make([]uint32, n),
		endByte:    make([]uint32, n),
		parent:     make([]uint32, n),
		firstChild: make([]uint32, n),
		childCount: make([]uint16, n),
		flags:      make([]uint8, n),
		childIdx:   make([]uint32, edges),
	}

	// Pass 2: assign preorder indices and fill arrays. Child indices are
	// allocated left-to-right (document order) before push, so the stack
	// (LIFO) is fed children in reverse to pop them back in document order.
	type frame struct {
		node *gotreesitter.Node
		idx  uint32
	}
	stack := make([]frame, 0, 256)
	stack = append(stack, frame{root, 0})
	t.parent[0] = soaNoParent
	next := uint32(1)
	edgeCursor := uint32(0)

	for len(stack) > 0 {
		last := len(stack) - 1
		f := stack[last]
		stack = stack[:last]
		node, i := f.node, f.idx

		t.symbol[i] = node.Symbol()
		t.startByte[i] = node.StartByte()
		t.endByte[i] = node.EndByte()
		var fl uint8
		if node.IsNamed() {
			fl |= soaFlagNamed
		}
		if node.IsMissing() {
			fl |= soaFlagMissing
		}
		if node.IsExtra() {
			fl |= soaFlagExtra
		}
		if node.HasError() {
			fl |= soaFlagHasError
		}
		t.flags[i] = fl

		cc := node.ChildCount()
		t.childCount[i] = uint16(cc)
		t.firstChild[i] = edgeCursor

		childIndices := make([]uint32, cc)
		for c := 0; c < cc; c++ {
			ci := next
			next++
			t.parent[ci] = i
			t.childIdx[edgeCursor+uint32(c)] = ci
			childIndices[c] = ci
		}
		edgeCursor += uint32(cc)

		for c := cc - 1; c >= 0; c-- {
			stack = append(stack, frame{node.Child(c), childIndices[c]})
		}
	}

	return t
}

// bytesTotal is the flat byte footprint of the SoA arrays: no per-node
// struct headers, no interior pointers other than the slice headers.
func (t *soaTree) bytesTotal() int64 {
	if t == nil {
		return 0
	}
	var total int64
	total += int64(len(t.symbol)) * 2 // Symbol = uint16
	total += int64(len(t.startByte)) * 4
	total += int64(len(t.endByte)) * 4
	total += int64(len(t.parent)) * 4
	total += int64(len(t.firstChild)) * 4
	total += int64(len(t.childCount)) * 2
	total += int64(len(t.flags)) * 1
	total += int64(len(t.childIdx)) * 4
	return total
}

func (t *soaTree) bytesPerNode() float64 {
	if t == nil || t.nodeCount == 0 {
		return 0
	}
	return float64(t.bytesTotal()) / float64(t.nodeCount)
}

// walk performs a full preorder DFS over the SoA arrays via index arithmetic
// only (no pointer chasing at all) and calls visit for every node index.
func (t *soaTree) walk(visit func(idx uint32)) {
	if t == nil || t.nodeCount == 0 {
		return
	}
	stack := make([]uint32, 0, 256)
	stack = append(stack, 0)
	for len(stack) > 0 {
		last := len(stack) - 1
		i := stack[last]
		stack = stack[:last]
		visit(i)
		start := t.firstChild[i]
		cc := t.childCount[i]
		for c := int(cc) - 1; c >= 0; c-- {
			stack = append(stack, t.childIdx[start+uint32(c)])
		}
	}
}
