package gotreesitter

func normalizeCornCompatibility(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || len(source) == 0 {
		return
	}
	walkResultTreePostorder(root, func(n *Node) {
		normalizeCornExtraErrorLeafFlags(n)
		normalizeCornQuotedWholePathSeg(n, source, lang)
	})
}

func normalizeCornExtraErrorLeafFlags(n *Node) {
	if n == nil || n.symbol != errorSymbol || !n.isExtra() || !n.hasError() {
		return
	}
	for i := 0; i < resultChildCount(n); i++ {
		child := resultChildAt(n, i)
		if child == nil || child.symbol == errorSymbol || resultChildCount(child) != 0 {
			continue
		}
		child.setHasError(false)
	}
}

func normalizeCornQuotedWholePathSeg(path *Node, source []byte, lang *Language) {
	if path == nil || path.Type(lang) != "path" || resultChildCount(path) != 3 {
		return
	}
	start, end := path.startByte, path.endByte
	if start >= end || int(end) > len(source) || source[start] != '\'' || source[end-1] != '\'' {
		return
	}

	left := resultChildAt(path, 0)
	dot := resultChildAt(path, 1)
	right := resultChildAt(path, 2)
	if left == nil || dot == nil || right == nil ||
		left.Type(lang) != "path_seg" ||
		dot.Type(lang) != "." ||
		right.Type(lang) != "path_seg" {
		return
	}
	if dot.startByte <= start || dot.endByte >= end || dot.startByte+1 != dot.endByte {
		return
	}
	if left.startByte != start || right.endByte != end {
		return
	}

	openQuote := cornQuoteLeafAt(left, lang, start, start+1)
	if openQuote == nil {
		return
	}
	closeQuote := cornQuoteLeafAt(right, lang, end-1, end)
	if closeQuote == nil {
		closeQuote = newLeafNodeInArena(path.ownerArena, openQuote.symbol, openQuote.isNamed(), end-1, end,
			advancePointByBytes(Point{}, source[:end-1]),
			advancePointByBytes(Point{}, source[:end]))
	}

	errNode := newParentNodeInArena(path.ownerArena, errorSymbol, true, []*Node{dot}, nil, 0)
	errNode.setHasError(true)
	errNode.setExtra(true)

	seg := newParentNodeInArena(path.ownerArena, left.symbol, left.isNamed(), []*Node{openQuote, errNode, closeQuote}, nil, left.productionID)
	seg.setHasError(true)
	replaceNodeChildrenUnfielded(path, []*Node{seg})
	path.setHasError(true)
}

func cornQuoteLeafAt(parent *Node, lang *Language, start, end uint32) *Node {
	for i := 0; i < resultChildCount(parent); i++ {
		child := resultChildAt(parent, i)
		if child != nil &&
			child.Type(lang) == "'" &&
			child.startByte == start &&
			child.endByte == end {
			return child
		}
	}
	return nil
}
