package gotreesitter

import "sync"

// WalkAction controls the tree walk behavior.
type WalkAction int

const (
	// WalkContinue continues the walk to children and siblings.
	WalkContinue WalkAction = iota
	// WalkSkipChildren skips the current node's children but continues to siblings.
	WalkSkipChildren
	// WalkStop terminates the walk entirely.
	WalkStop
)

type walkEntry struct {
	node  *Node
	depth int
}

// walkPool reuses stack backing arrays across Walk calls to eliminate the
// per-call heap allocation for the traversal stack.
var walkPool = sync.Pool{
	New: func() any {
		s := make([]walkEntry, 0, 64)
		return &s
	},
}

// Walk performs a depth-first traversal of the syntax tree rooted at node.
// The callback receives each node and its depth (0 for the starting node).
// Return WalkSkipChildren to skip a node's children, or WalkStop to end early.
func Walk(node *Node, fn func(node *Node, depth int) WalkAction) {
	if node == nil {
		return
	}

	sp := walkPool.Get().(*[]walkEntry)
	stack := (*sp)[:0]
	// defer captures stack by reference so the pool receives the final
	// (possibly grown) slice, and the stack is returned even if fn panics.
	defer func() {
		*sp = stack[:0]
		walkPool.Put(sp)
	}()

	stack = append(stack, walkEntry{node: node, depth: 0})
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		action := fn(e.node, e.depth)
		if action == WalkStop {
			break
		}
		if action == WalkSkipChildren {
			continue
		}

		// Use Child(i) index access to avoid allocating a []*Node children slice.
		childDepth := e.depth + 1
		count := nodeChildCountNoMaterialize(e.node)
		for i := count - 1; i >= 0; i-- {
			if child := nodeChildAtForReason(e.node, i, materializeForCursor); child != nil {
				stack = append(stack, walkEntry{node: child, depth: childDepth})
			}
		}
	}
}
