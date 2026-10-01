package gotreesitter

func normalizeDTDCompatibility(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dtd" || len(source) == 0 {
		return
	}
	nameSym, ok := symbolByName(lang, "Name")
	if !ok {
		return
	}
	contentSpecSym, hasContentSpec := symbolByName(lang, "contentspec")
	peRefSym, hasPERef := symbolByName(lang, "PEReference")
	percentSym, hasPercent := symbolByName(lang, "%")
	semiSym, hasSemi := symbolByName(lang, ";")
	walkResultTreePostorder(root, func(n *Node) {
		if hasContentSpec && hasPERef && hasPercent && hasSemi {
			normalizeDTDElementDeclRecoveredPEReference(n, source, lang, dtdResultSymbols{
				name:        nameSym,
				contentSpec: contentSpecSym,
				peRef:       peRefSym,
				percent:     percentSym,
				semi:        semiSym,
			})
		}
	})
}

type dtdResultSymbols struct {
	name        Symbol
	contentSpec Symbol
	peRef       Symbol
	percent     Symbol
	semi        Symbol
}

func normalizeDTDElementDeclRecoveredPEReference(n *Node, source []byte, lang *Language, syms dtdResultSymbols) bool {
	if n == nil || n.Type(lang) != "elementdecl" || resultChildCount(n) == 0 {
		return false
	}
	children := resultChildSliceForMutation(n)
	rewritten := make([]*Node, 0, len(children)+1)
	changed := false
	for i := 0; i < len(children); i++ {
		child := children[i]
		nameSpan, peSpan, ok := dtdResultRecoveredNamePESpans(child, source)
		if !ok {
			rewritten = append(rewritten, child)
			continue
		}
		name := dtdResultLeaf(n.ownerArena, source, syms.name, symbolIsNamed(lang, syms.name), nameSpan[0], nameSpan[1])
		peRef := dtdResultPEReference(n.ownerArena, source, lang, syms, peSpan)
		contentSpec := newParentNodeInArena(n.ownerArena, syms.contentSpec, symbolIsNamed(lang, syms.contentSpec), []*Node{peRef}, nil, 0)
		rewritten = append(rewritten, name, contentSpec)
		if i+1 < len(children) && dtdResultDroppedElementContentName(children[i+1], source, lang, peSpan[1]) {
			errNode := dtdResultElementContentError(n.ownerArena, source, lang, syms.name, children[i+1])
			rewritten = append(rewritten, errNode)
			i++
			if i+1 < len(children) && dtdResultBogusElementContentSpecAfterDroppedName(children[i+1], source, lang, errNode.endByte) {
				i++
			}
		}
		changed = true
	}
	if !changed {
		return false
	}
	replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(n.ownerArena, rewritten))
	n.setHasError(true)
	return true
}

func dtdResultDroppedElementContentName(n *Node, source []byte, lang *Language, prevEnd uint32) bool {
	if n == nil || n.Type(lang) != "Name" || n.startByte < prevEnd || int(n.endByte) > len(source) {
		return false
	}
	if dtdResultSkipWhitespace(source, prevEnd, n.startByte) != n.startByte {
		return false
	}
	text := source[n.startByte:n.endByte]
	return string(text) == "EMPTY" || string(text) == "ANY"
}

func dtdResultBogusElementContentSpecAfterDroppedName(n *Node, source []byte, lang *Language, prevEnd uint32) bool {
	if n == nil || n.Type(lang) != "contentspec" || n.startByte < prevEnd || int(n.endByte) > len(source) {
		return false
	}
	return dtdResultSkipWhitespace(source, prevEnd, n.startByte) == n.startByte
}

func dtdResultRecoveredNamePESpans(n *Node, source []byte) (nameSpan, peSpan [2]uint32, ok bool) {
	if n == nil || n.symbol != errorSymbol || n.startByte >= n.endByte || int(n.endByte) > len(source) {
		return nameSpan, peSpan, false
	}
	nameEnd := dtdResultNameEnd(source, n.startByte, n.endByte)
	if nameEnd <= n.startByte || nameEnd >= n.endByte {
		return nameSpan, peSpan, false
	}
	peStart, peEnd, ok := dtdResultTrailingPEReferenceSpan(source, nameEnd, n.endByte)
	if !ok {
		return nameSpan, peSpan, false
	}
	return [2]uint32{n.startByte, nameEnd}, [2]uint32{peStart, peEnd}, true
}

func dtdResultNameEnd(source []byte, start, limit uint32) uint32 {
	if int(start) >= len(source) || start >= limit || !dtdResultNameStartByte(source[start]) {
		return start
	}
	pos := start + 1
	for pos < limit && int(pos) < len(source) && dtdResultNameByte(source[pos]) {
		pos++
	}
	return pos
}

func dtdResultTrailingPEReferenceSpan(source []byte, start, end uint32) (uint32, uint32, bool) {
	pos := dtdResultSkipWhitespace(source, start, end)
	if pos >= end || int(pos) >= len(source) || source[pos] != '%' {
		return 0, 0, false
	}
	peStart := pos
	pos++
	refStart := pos
	for pos < end && int(pos) < len(source) && dtdResultNameByte(source[pos]) {
		pos++
	}
	if pos == refStart || pos >= end || int(pos) >= len(source) || source[pos] != ';' {
		return 0, 0, false
	}
	pos++
	peEnd := pos
	if dtdResultSkipWhitespace(source, pos, end) != end {
		return 0, 0, false
	}
	return peStart, peEnd, true
}

func dtdResultSkipWhitespace(source []byte, start, end uint32) uint32 {
	pos := start
	for pos < end && int(pos) < len(source) {
		switch source[pos] {
		case ' ', '\t', '\n', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func dtdResultPEReference(arena *nodeArena, source []byte, lang *Language, syms dtdResultSymbols, span [2]uint32) *Node {
	nameStart := span[0] + 1
	nameEnd := dtdResultNameEnd(source, nameStart, span[1])
	children := []*Node{
		dtdResultLeaf(arena, source, syms.percent, symbolIsNamed(lang, syms.percent), span[0], span[0]+1),
		dtdResultLeaf(arena, source, syms.name, symbolIsNamed(lang, syms.name), nameStart, nameEnd),
		dtdResultLeaf(arena, source, syms.semi, symbolIsNamed(lang, syms.semi), span[1]-1, span[1]),
	}
	return newParentNodeInArena(arena, syms.peRef, symbolIsNamed(lang, syms.peRef), children, nil, 0)
}

func dtdResultLeaf(arena *nodeArena, source []byte, sym Symbol, named bool, start, end uint32) *Node {
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	return newLeafNodeInArena(arena, sym, named, start, end, startPoint, endPoint)
}

func dtdResultElementContentError(arena *nodeArena, source []byte, lang *Language, nameSym Symbol, nameNode *Node) *Node {
	name := dtdResultLeaf(arena, source, nameSym, symbolIsNamed(lang, nameSym), nameNode.startByte, nameNode.endByte)
	n := newParentNodeInArena(arena, errorSymbol, true, []*Node{name}, nil, 0)
	n.endByte = dtdResultSkipWhitespace(source, nameNode.endByte, uint32(len(source)))
	n.endPoint = advancePointByBytes(n.startPoint, source[n.startByte:n.endByte])
	n.setHasError(true)
	n.setExtra(true)
	return n
}

func dtdResultNameStartByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_' || b == ':'
}

func dtdResultNameByte(b byte) bool {
	return dtdResultNameStartByte(b) || (b >= '0' && b <= '9') || b == '.' || b == '-' || b == 0xb7
}
