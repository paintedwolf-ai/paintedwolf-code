package gotreesitter

type csharpPropertyInsertion struct {
	node *Node
}

func normalizeCSharpMissingAttributedProperties(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" || len(source) == 0 {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "declaration_list" {
			csharpInsertMissingAttributedProperties(n, source, lang)
		}
	})
}

func csharpInsertMissingAttributedProperties(declList *Node, source []byte, lang *Language) bool {
	if declList == nil || declList.ownerArena == nil || int(declList.endByte) > len(source) || declList.startByte >= declList.endByte {
		return false
	}
	openBrace := declList.startByte
	if source[openBrace] != '{' {
		found := false
		for i := declList.startByte; i < declList.endByte; i++ {
			if source[i] == '{' {
				openBrace = i
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	closeBrace := declList.endByte - 1
	if source[closeBrace] != '}' {
		found := false
		for i := declList.endByte; i > openBrace; i-- {
			if source[i-1] == '}' {
				closeBrace = i - 1
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	spans := csharpTopLevelChunkSpans(source[openBrace+1 : closeBrace])
	if len(spans) == 0 {
		return false
	}
	insertions := make([]csharpPropertyInsertion, 0, 1)
	for _, rel := range spans {
		start := openBrace + 1 + rel[0]
		end := openBrace + 1 + rel[1]
		if csharpDeclarationListHasNamedChildContainedIn(declList, start, end) {
			continue
		}
		if prop, ok := csharpRecoverAttributedAutoPropertyFromRange(source, start, end, lang, declList.ownerArena); ok {
			insertions = append(insertions, csharpPropertyInsertion{node: prop})
			continue
		}
		if method, ok := csharpRecoverAttributedMethodDeclarationFromRange(source, start, end, lang, declList.ownerArena); ok {
			insertions = append(insertions, csharpPropertyInsertion{node: method})
			continue
		}
	}
	if len(insertions) == 0 {
		return false
	}
	rebuilt := make([]*Node, 0, len(declList.children)+len(insertions))
	insertIdx := 0
	for _, child := range declList.children {
		for insertIdx < len(insertions) && (child == nil || insertions[insertIdx].node.startByte <= child.startByte) {
			rebuilt = append(rebuilt, insertions[insertIdx].node)
			insertIdx++
		}
		if child != nil {
			if csharpNodeOverlapsInsertions(child, insertions) {
				continue
			}
			rebuilt = append(rebuilt, child)
		}
	}
	for insertIdx < len(insertions) {
		rebuilt = append(rebuilt, insertions[insertIdx].node)
		insertIdx++
	}
	children := declList.ownerArena.allocNodeSlice(len(rebuilt))
	copy(children, rebuilt)
	replaceNodeChildrenUnfielded(declList, children)
	declList.setHasError(false)
	return true
}

func csharpNodeOverlapsInsertions(child *Node, insertions []csharpPropertyInsertion) bool {
	if child == nil || !child.IsNamed() {
		return false
	}
	for _, ins := range insertions {
		if ins.node == nil {
			continue
		}
		if child.startByte < ins.node.endByte && child.endByte > ins.node.startByte {
			return true
		}
	}
	return false
}

func csharpDeclarationListHasNamedChildContainedIn(declList *Node, start, end uint32) bool {
	for _, child := range declList.children {
		if child == nil || !child.IsNamed() {
			continue
		}
		if child.startByte >= start && child.endByte <= end {
			return true
		}
	}
	return false
}

func csharpRecoverAttributedAutoPropertyFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '[' || end == 0 || source[end-1] != '}' {
		return nil, false
	}
	attributeLists, cursor, ok := csharpBuildLeadingAttributeListsFromSource(source, start, end, lang, arena)
	if !ok || len(attributeLists) == 0 {
		return nil, false
	}
	modStart, modEnd, ok := csharpScanIdentifierAt(source, cursor)
	if !ok || string(source[modStart:modEnd]) != "public" {
		return nil, false
	}
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, modEnd))
	if !ok {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, typeEnd))
	if !ok {
		return nil, false
	}
	accessorStart := csharpSkipSpaceBytes(source, nameEnd)
	if accessorStart >= end || source[accessorStart] != '{' {
		return nil, false
	}
	accessorEnd := csharpFindMatchingBraceByte(source, int(accessorStart), int(end))
	if accessorEnd < 0 || uint32(accessorEnd+1) != end {
		return nil, false
	}
	modifier, ok := csharpBuildLeafNodeByName(arena, source, lang, "modifier", modStart, modEnd)
	if !ok {
		return nil, false
	}
	typeNode, ok := csharpBuildLambdaParameterTypeNode(arena, source, lang, typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	accessors, ok := csharpBuildAccessorListNode(source, accessorStart, uint32(accessorEnd+1), lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "property_declaration")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	children := make([]*Node, 0, len(attributeLists)+4)
	children = append(children, attributeLists...)
	children = append(children, modifier, typeNode, nameNode, accessors)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	return newParentNodeInArena(arena, sym, named, buf, nil, 0), true
}

func csharpRecoverAttributedMethodDeclarationFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '[' || end == 0 || source[end-1] != '}' {
		return nil, false
	}
	attributeLists, cursor, ok := csharpBuildLeadingAttributeListsFromSource(source, start, end, lang, arena)
	if !ok || len(attributeLists) == 0 {
		return nil, false
	}
	modStart, modEnd, ok := csharpScanIdentifierAt(source, cursor)
	if !ok || string(source[modStart:modEnd]) != "public" {
		return nil, false
	}
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, modEnd))
	if !ok {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, typeEnd))
	if !ok {
		return nil, false
	}
	paramsStart := csharpSkipSpaceBytes(source, nameEnd)
	if paramsStart >= end || source[paramsStart] != '(' {
		return nil, false
	}
	paramsEnd, ok := csharpFindMatchingParenByte(source, paramsStart, end)
	if !ok {
		return nil, false
	}
	blockStart := csharpSkipSpaceBytes(source, paramsEnd+1)
	if blockStart >= end || source[blockStart] != '{' {
		return nil, false
	}
	blockEnd := csharpFindMatchingBraceByte(source, int(blockStart), int(end))
	if blockEnd < 0 || uint32(blockEnd+1) != end {
		return nil, false
	}
	modifier, ok := csharpBuildLeafNodeByName(arena, source, lang, "modifier", modStart, modEnd)
	if !ok {
		return nil, false
	}
	returnType, ok := csharpBuildLambdaParameterTypeNode(arena, source, lang, typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	params, ok := csharpBuildMethodParameterListNode(source, paramsStart, paramsEnd+1, lang, arena)
	if !ok {
		return nil, false
	}
	block, ok := csharpBuildEmptyBlockNode(source, blockStart, uint32(blockEnd+1), lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "method_declaration")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	children := make([]*Node, 0, len(attributeLists)+5)
	children = append(children, attributeLists...)
	children = append(children, modifier, returnType, nameNode, params, block)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	return newParentNodeInArena(arena, sym, named, buf, nil, 0), true
}

func csharpBuildAccessorListNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '{' || source[end-1] != '}' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "accessor_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "{", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "}", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	cursor := csharpSkipSpaceBytes(source, start+1)
	for cursor < end-1 {
		itemStart := cursor
		identStart, identEnd, ok := csharpScanIdentifierAt(source, itemStart)
		if !ok || identStart != itemStart {
			return nil, false
		}
		semi := csharpSkipSpaceBytes(source, identEnd)
		if semi >= end-1 || source[semi] != ';' {
			return nil, false
		}
		accessor, ok := csharpBuildAccessorDeclarationNode(source, itemStart, semi+1, lang, arena)
		if !ok {
			return nil, false
		}
		children = append(children, accessor)
		cursor = csharpSkipSpaceBytes(source, semi+1)
	}
	children = append(children, closeTok)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, buf, nil, 0), true
}

func csharpBuildAccessorDeclarationNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	sym, ok := symbolByName(lang, "accessor_declaration")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newLeafNodeInArena(arena, sym, named, start, end, advancePointByBytes(Point{}, source[:start]), advancePointByBytes(Point{}, source[:end])), true
}

func csharpBuildMethodParameterListNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '(' || source[end-1] != ')' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "parameter_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "(", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ")", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	contentStart, contentEnd := csharpTrimSpaceBounds(source, start+1, end-1)
	if contentStart < contentEnd {
		param, ok := csharpBuildMethodParameterNode(source, contentStart, contentEnd, lang, arena)
		if !ok {
			return nil, false
		}
		children = append(children, param)
	}
	children = append(children, closeTok)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, buf, nil, 0), true
}

func csharpBuildMethodParameterNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	attributeLists, cursor, ok := csharpBuildLeadingAttributeListsFromSource(source, start, end, lang, arena)
	if !ok {
		cursor = start
	}
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, cursor)
	if !ok {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, typeEnd))
	if !ok || csharpSkipSpaceBytes(source, nameEnd) != end {
		return nil, false
	}
	typeNode, ok := csharpBuildLambdaParameterTypeNode(arena, source, lang, typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "parameter")
	if !ok {
		return nil, false
	}
	children := make([]*Node, 0, len(attributeLists)+2)
	children = append(children, attributeLists...)
	children = append(children, typeNode, nameNode)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, buf, nil, 0), true
}

func csharpBuildEmptyBlockNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '{' || source[end-1] != '}' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "block")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "{", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "}", end-1, end)
	if !ok {
		return nil, false
	}
	children := arena.allocNodeSlice(2)
	children[0] = openTok
	children[1] = closeTok
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}
