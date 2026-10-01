package gotreesitter

func normalizeWolframCompatibility(root *Node, source []byte, lang *Language) {
	normalizeWolframSplitInfixRoot(root, source, lang)
}

func normalizeWolframSplitInfixRoot(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "wolfram" || root.Type(lang) != "source_file" || len(source) == 0 {
		return
	}
	infixSym, infixNamed, ok := symbolMeta(lang, "infix")
	if !ok {
		return
	}
	children := resultChildSliceForMutation(root)
	if len(children) < 2 {
		return
	}
	out := make([]*Node, 0, len(children))
	changed := false
	for i := 0; i < len(children); i++ {
		left := children[i]
		if i+1 >= len(children) {
			out = append(out, left)
			continue
		}
		prefix := children[i+1]
		if wolframCanMergeSplitInfix(left, prefix, source, lang) {
			prefixChildren := resultChildSliceForMutation(prefix)
			mergedChildren := cloneNodeSliceIfArena(root.ownerArena, []*Node{left, prefixChildren[0], prefixChildren[1]})
			merged := newParentNodeInArena(root.ownerArena, infixSym, infixNamed, mergedChildren, nil, prefix.productionID)
			out = append(out, merged)
			i++
			changed = true
			continue
		}
		out = append(out, left)
	}
	if changed {
		replaceNodeChildrenUnfielded(root, cloneNodeSliceIfArena(root.ownerArena, out))
	}
}

func wolframCanMergeSplitInfix(left, prefix *Node, source []byte, lang *Language) bool {
	if left == nil || prefix == nil || lang == nil || prefix.Type(lang) != "prefix" {
		return false
	}
	if !wolframIsInfixOperand(left, lang) {
		return false
	}
	children := resultChildSliceForMutation(prefix)
	if len(children) != 2 || !wolframIsInfixOperand(children[1], lang) {
		return false
	}
	op := children[0]
	if op == nil || op.IsNamed() || op.startByte < left.endByte || op.endByte > prefix.endByte {
		return false
	}
	if !wolframIsInfixOperator(source, op.startByte, op.endByte) {
		return false
	}
	return bytesAreTrivia(source[left.endByte:op.startByte]) && bytesAreTrivia(source[op.endByte:children[1].startByte])
}

func wolframIsInfixOperand(n *Node, lang *Language) bool {
	switch n.Type(lang) {
	case "symbol", "integer", "real", "string":
		return true
	default:
		return false
	}
}

func wolframIsInfixOperator(source []byte, start, end uint32) bool {
	if int(end) > len(source) || start >= end {
		return false
	}
	switch string(source[start:end]) {
	case "+", "-", "*", ".", "**", "<>", "/*", "@*", "/", "^", "/@", "@@", "//@", "@@@", "@", "?",
		",", ";", "~~", "|", "||", "&&", "===", "=!=", "==", "!=", "<", "<=", ">", ">=",
		"=", ":=", "^=", "^:=", "|->", "//", "//=", "+=", "-=", "*=", "/=", "/.", "//.", "->", ":>", "<->", "/;":
		return true
	default:
		return false
	}
}
