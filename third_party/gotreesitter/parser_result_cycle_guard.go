package gotreesitter

// recoveredResultNodesAcyclic validates the recovered result forest without
// materializing or rewriting any child storage. It uses an iterative three-
// color walk so an already-corrupt graph terminates deterministically instead
// of recursing forever. Non-node compact/pending payloads are not materialized;
// they cannot carry a Node back-edge until their normal materialization step.
func recoveredResultNodesAcyclic(roots []*Node) bool {
	const (
		resultNodeGray  = uint8(1)
		resultNodeBlack = uint8(2)
	)
	type frame struct {
		n    *Node
		next int
	}

	color := make(map[*Node]uint8, len(roots)*4)
	stack := make([]frame, 0, 64)
	for _, root := range roots {
		if root == nil || color[root] == resultNodeBlack {
			continue
		}
		color[root] = resultNodeGray
		stack = append(stack, frame{n: root})
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next >= nodeChildCountNoMaterialize(top.n) {
				color[top.n] = resultNodeBlack
				stack = stack[:len(stack)-1]
				continue
			}
			entry, ok := nodeChildEntryAtNoMaterialize(top.n, top.next)
			top.next++
			if !ok {
				continue
			}
			child := stackEntryNode(entry)
			if child == nil {
				continue
			}
			switch color[child] {
			case resultNodeGray:
				return false
			case resultNodeBlack:
				continue
			default:
				color[child] = resultNodeGray
				stack = append(stack, frame{n: child})
			}
		}
	}
	return true
}

// recoveredResultInvariantErrorTree fails closed when recovered result nodes
// contain a Node back-edge. The corrupt graph is neither repaired nor exposed:
// callers receive a standalone ERROR tree whose public stop reason identifies
// the invariant failure.
func recoveredResultInvariantErrorTree(nodes []*Node, source []byte, lang *Language, arena *nodeArena) *Tree {
	if recoveredResultNodesAcyclic(nodes) {
		return nil
	}
	tree := parseErrorTreeWithArena(source, lang, arena)
	tree.setParseStopReason(ParseStopInvariantViolation)
	return tree
}
