package gotreesitter

// normalizeSolidityMemberObjectWrappers collapses the redundant `expression`
// wrapper Go emits around the object operand of a `member_expression`.
//
// For `a.b`, the C tree-sitter-solidity oracle builds:
//
//	member_expression [ identifier(object) "." identifier(property) ]
//
// Go's GLR build instead wraps the object identifier in a unary `expression`
// node spanning the identical bytes:
//
//	member_expression [ expression(identifier)(object) "." identifier(property) ]
//
// When the object child is exactly such a single-identifier `expression` over
// the same span, this pass replaces it with the bare inner identifier so the
// shape matches C. The guard (single named identifier child, identical span)
// keeps it from touching genuine compound objects like `a.b.c` or `f().g`,
// whose object operand is itself a member/call expression, not a lone
// identifier wrapper.
func normalizeSolidityMemberObjectWrappers(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "solidity" {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) != "member_expression" {
			return
		}
		for i, child := range n.children {
			if child == nil || child.Type(lang) != "expression" {
				continue
			}
			if len(child.children) != 1 {
				continue
			}
			inner := child.children[0]
			if inner == nil || inner.Type(lang) != "identifier" {
				continue
			}
			// Only collapse a pure unary wrapper: the expression must add no
			// span of its own (no leading/trailing trivia captured) over the
			// inner identifier.
			if inner.startByte != child.startByte || inner.endByte != child.endByte {
				continue
			}
			inner.parent = n
			inner.childIndex = int32(i)
			n.children[i] = inner
		}
	})
}

// normalizeSolidityCallExpressionAliases rewrites generated-only
// constructor/type-cast call wrappers to match the C oracle.
//
// Upstream tree-sitter-solidity's generated C parser reports call-shaped
// `new Type(...)` and `uint256(...)` expressions as `call_expression`, with
// the specific `new_expression` / `type_cast_expression` node wrapped as the
// callee expression. The Go grammargen path keeps the specific node as the
// outer wrapper. Rewrite only cases with an explicit `call_argument` child so
// bare `new Type` or non-call type expressions keep their grammar-specific
// shape.
func normalizeSolidityCallExpressionAliases(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "solidity" {
		return
	}
	callSym, callNamed, ok := symbolMeta(lang, "call_expression")
	if !ok {
		return
	}
	exprSym, exprNamed, ok := symbolMeta(lang, "expression")
	if !ok {
		return
	}
	walkResultTree(root, func(n *Node) {
		oldSym := n.symbol
		oldNamed := n.isNamed()
		nodeType := n.Type(lang)
		switch n.Type(lang) {
		case "new_expression", "type_cast_expression":
		default:
			return
		}
		callArgIndex := solidityCallArgumentIndex(n, lang)
		if callArgIndex <= 0 {
			return
		}
		children := resultChildSliceForMutation(n)
		if callArgIndex >= len(children) {
			return
		}
		arena := n.ownerArena
		var wrapper *Node
		outerTailIndex := callArgIndex
		if nodeType == "type_cast_expression" && len(children) > 0 {
			wrapper = newParentNodeInArena(arena, exprSym, exprNamed, []*Node{children[0]}, nil, 0)
			outerTailIndex = 1
		} else {
			innerChildren := cloneNodeSliceIfArena(arena, children[:callArgIndex])
			inner := newParentNodeInArena(arena, oldSym, oldNamed, innerChildren, nil, n.productionID)
			wrapper = newParentNodeInArena(arena, exprSym, exprNamed, []*Node{inner}, nil, 0)
		}
		callChildren := make([]*Node, 0, len(children)-callArgIndex+1)
		callChildren = append(callChildren, wrapper)
		callChildren = append(callChildren, children[outerTailIndex:]...)
		n.symbol = callSym
		n.setNamed(callNamed)
		replaceNodeChildrenUnfielded(n, cloneNodeSliceIfArena(arena, callChildren))
	})
}

func solidityCallArgumentIndex(n *Node, lang *Language) int {
	for i := 0; i < resultChildCount(n); i++ {
		if child := resultChildAt(n, i); child != nil && child.Type(lang) == "call_argument" {
			return i
		}
	}
	return -1
}
