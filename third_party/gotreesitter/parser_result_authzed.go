package gotreesitter

import "bytes"

func normalizeAuthzedCompatibility(root *Node, source []byte, lang *Language) {
	normalizeAuthzedCleanRootShape(root, source, lang)
	normalizeAuthzedObjectCaveatRecovery(root, source, lang)
	normalizeAuthzedUnclosedCaveatRecovery(root, source, lang)
	normalizeAuthzedStrayCaveatTailRecovery(root, source, lang)
	normalizeAuthzedSingleQuotedCaveatRecovery(root, source, lang)
	normalizeAuthzedSingleQuotedCaveatBlockRecovery(root, source, lang)
	normalizeAuthzedUnsupportedUseDirective(root, source, lang)
	normalizeAuthzedMalformedDefinitionRoot(root, source, lang)
	normalizeAuthzedMissingPermissionExpression(root, source, lang)
	normalizeAuthzedWholeRootErrorTrivia(root, source, lang)
}

func normalizeAuthzedCleanRootShape(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || root.hasError() || symbolTypeName(lang, root.symbol) != "source_file" {
		return
	}
	children := resultChildSliceForMutation(root)
	if len(children) == 0 {
		return
	}
	out := make([]*Node, 0, len(children))
	changed := false
	for i := 0; i < len(children); {
		child := children[i]
		if child == nil {
			i++
			continue
		}
		switch symbolTypeName(lang, child.symbol) {
		case "_whitespace":
			changed = true
			i++
			continue
		case "definition_literal":
			if def, next, ok := authzedDefinitionFromFlatRootChildren(children, i, source, root.ownerArena, lang); ok {
				out = append(out, def)
				changed = true
				i = next
				continue
			}
		}
		out = append(out, child)
		i++
	}
	if !changed {
		return
	}
	if len(source) == 0 || source[len(source)-1] != '\n' {
		if eof := authzedEOFLeaf(root.ownerArena, lang, source); eof != nil {
			out = append(out, eof)
		}
	}
	root.children = cloneNodeSliceInArena(root.ownerArena, out)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(false)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

func authzedDefinitionFromFlatRootChildren(children []*Node, start int, source []byte, arena *nodeArena, lang *Language) (*Node, int, bool) {
	if start < 0 || start >= len(children) || children[start] == nil || symbolTypeName(lang, children[start].symbol) != "definition_literal" {
		return nil, start, false
	}
	defSym, ok := symbolByName(lang, "definition")
	if !ok {
		return nil, start, false
	}
	blockSym, ok := symbolByName(lang, "block")
	if !ok {
		return nil, start, false
	}
	nextContent := func(i int) int {
		for i < len(children) {
			if children[i] == nil {
				i++
				continue
			}
			switch symbolTypeName(lang, children[i].symbol) {
			case "_whitespace", "\n":
				i++
				continue
			default:
				return i
			}
		}
		return -1
	}
	nameIdx := nextContent(start + 1)
	if nameIdx < 0 || symbolTypeName(lang, children[nameIdx].symbol) != "identifier" {
		return nil, start, false
	}
	lbraceIdx := nextContent(nameIdx + 1)
	if lbraceIdx < 0 || symbolTypeName(lang, children[lbraceIdx].symbol) != "{" {
		return nil, start, false
	}
	blockChildren := make([]*Node, 0, 6)
	blockChildren = append(blockChildren, children[lbraceIdx])
	for i := lbraceIdx + 1; i < len(children); i++ {
		child := children[i]
		if child == nil {
			continue
		}
		switch symbolTypeName(lang, child.symbol) {
		case "_whitespace", "\n":
			continue
		case "}":
			blockChildren = append(blockChildren, child)
			block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), cloneNodeSliceInArena(arena, blockChildren), nil, 0)
			authzedSetNodeRange(block, source, int(children[lbraceIdx].startByte), int(child.endByte))
			defChildren := cloneNodeSliceInArena(arena, []*Node{children[start], children[nameIdx], block})
			def := newParentNodeInArena(arena, defSym, symbolIsNamed(lang, defSym), defChildren, authzedDefinitionFieldIDs(arena, lang), 0)
			authzedSetNodeRange(def, source, int(children[start].startByte), int(child.endByte))
			return def, i + 1, true
		case "relation", "permission":
			blockChildren = append(blockChildren, child)
		default:
			return nil, start, false
		}
	}
	return nil, start, false
}

func normalizeAuthzedUnclosedCaveatRecovery(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	children := authzedRecoveryChildren(root, lang)
	caveatIdx := authzedFindDirectChild(children, lang, "caveat_literal", 0)
	if caveatIdx < 0 {
		caveatIdx = authzedFindDirectChildText(children, source, "caveat", 0)
	}
	if caveatIdx < 0 || caveatIdx+5 >= len(children) {
		return
	}
	caveatLiteral := children[caveatIdx]
	if symbolTypeName(lang, caveatLiteral.symbol) != "caveat_literal" {
		caveatLiteral = authzedLeafByName(root.ownerArena, lang, "caveat_literal", source, int(children[caveatIdx].startByte), int(children[caveatIdx].endByte))
		if caveatLiteral == nil {
			return
		}
	}
	name := children[caveatIdx+1]
	params := children[caveatIdx+2]
	lbrace := children[caveatIdx+3]
	exprIdent := children[caveatIdx+4]
	rawExpr := children[caveatIdx+5]
	if symbolTypeName(lang, name.symbol) != "identifier" ||
		symbolTypeName(lang, params.symbol) != "parameters_list" ||
		symbolTypeName(lang, lbrace.symbol) != "{" ||
		symbolTypeName(lang, exprIdent.symbol) != "identifier" ||
		symbolTypeName(lang, rawExpr.symbol) != "_expression" ||
		!rawExpr.hasError() ||
		resultChildCount(rawExpr) != 1 {
		return
	}
	rawErr := resultChildAt(rawExpr, 0)
	if rawErr == nil || rawErr.symbol != errorSymbol {
		return
	}
	errorStart := int(rawErr.startByte)
	errorEnd := int(rawErr.endByte)
	if errorEnd != errorStart+1 || errorStart >= len(source) || source[errorStart] != '{' {
		return
	}
	newlineStart := errorEnd
	if newlineStart >= len(source) || source[newlineStart] != '\n' {
		return
	}
	rbraceStart := authzedNextByte(source, newlineStart+1, '}')
	if rbraceStart < 0 {
		return
	}

	arena := root.ownerArena
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	caveatSym, ok := symbolByName(lang, "caveat")
	if !ok {
		return
	}
	blockSym, ok := symbolByName(lang, "block_c")
	if !ok {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}

	exprStmt, ok := authzedCaveatExpressionStatement([]*Node{exprIdent}, lang, arena)
	if !ok {
		return
	}
	lbraceErr := authzedLeafByName(arena, lang, "{", source, errorStart, errorEnd)
	errNode := authzedExtraError(arena, source, errorStart, errorEnd, []*Node{lbraceErr})
	newline := authzedLeaf(arena, lang, newlineSym, false, source, newlineStart, newlineStart+1)
	rbrace := authzedLeafByName(arena, lang, "}", source, rbraceStart, rbraceStart+1)
	if lbraceErr == nil || errNode == nil || newline == nil || rbrace == nil {
		return
	}

	block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), cloneNodeSliceInArena(arena, []*Node{lbrace, exprStmt, errNode, newline, rbrace}), nil, 0)
	block.setHasError(true)
	authzedSetNodeRange(block, source, int(lbrace.startByte), rbraceStart+1)
	caveat := newParentNodeInArena(arena, caveatSym, symbolIsNamed(lang, caveatSym), cloneNodeSliceInArena(arena, []*Node{caveatLiteral, name, params, block}), authzedCaveatFieldIDs(arena, lang), 0)
	caveat.setHasError(true)
	authzedSetNodeRange(caveat, source, int(caveatLiteral.startByte), rbraceStart+1)

	rootChildren := make([]*Node, 0, caveatIdx+4)
	rootChildren = append(rootChildren, children[:caveatIdx]...)
	rootChildren = append(rootChildren, caveat)
	rootChildren = authzedAppendRootTailFromSource(rootChildren, source, arena, lang, rbraceStart+1)

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

func normalizeAuthzedMissingPermissionExpression(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	children := authzedRecoveryChildren(root, lang)
	if len(children) != 6 ||
		symbolTypeName(lang, children[0].symbol) != "definition_literal" ||
		symbolTypeName(lang, children[1].symbol) != "identifier" ||
		symbolTypeName(lang, children[2].symbol) != "{" ||
		symbolTypeName(lang, children[3].symbol) != "permission_literal" ||
		symbolTypeName(lang, children[4].symbol) != "identifier" ||
		symbolTypeName(lang, children[5].symbol) != "=" {
		return
	}
	rbraceStart := authzedNextByte(source, int(children[5].endByte), '}')
	if rbraceStart < 0 {
		return
	}
	arena := root.ownerArena
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	defSym, ok := symbolByName(lang, "definition")
	if !ok {
		return
	}
	blockSym, ok := symbolByName(lang, "block")
	if !ok {
		return
	}
	permissionSym, ok := symbolByName(lang, "permission")
	if !ok {
		return
	}
	permExprSym, ok := symbolByName(lang, "perm_expression")
	if !ok {
		return
	}
	identSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return
	}
	missingIdent := authzedLeaf(arena, lang, identSym, symbolIsNamed(lang, identSym), source, rbraceStart, rbraceStart)
	if missingIdent == nil {
		return
	}
	missingIdent.setMissing(true)
	missingIdent.setHasError(true)
	permExpr := newParentNodeInArena(arena, permExprSym, symbolIsNamed(lang, permExprSym), cloneNodeSliceInArena(arena, []*Node{missingIdent}), nil, 0)
	permExpr.setHasError(true)
	authzedSetNodeRange(permExpr, source, rbraceStart, rbraceStart)
	permission := newParentNodeInArena(arena, permissionSym, symbolIsNamed(lang, permissionSym), cloneNodeSliceInArena(arena, []*Node{children[3], children[4], children[5], permExpr}), authzedPermissionFieldIDs(arena, lang), 0)
	permission.setHasError(true)
	authzedSetNodeRange(permission, source, int(children[3].startByte), rbraceStart)
	rbrace := authzedLeafByName(arena, lang, "}", source, rbraceStart, rbraceStart+1)
	if rbrace == nil {
		return
	}
	block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), cloneNodeSliceInArena(arena, []*Node{children[2], permission, rbrace}), nil, 0)
	block.setHasError(true)
	authzedSetNodeRange(block, source, int(children[2].startByte), rbraceStart+1)
	definition := newParentNodeInArena(arena, defSym, symbolIsNamed(lang, defSym), cloneNodeSliceInArena(arena, []*Node{children[0], children[1], block}), authzedDefinitionFieldIDs(arena, lang), 0)
	definition.setHasError(true)
	authzedSetNodeRange(definition, source, int(children[0].startByte), rbraceStart+1)
	rootChildren := []*Node{definition}
	if eof := authzedEOFLeaf(arena, lang, source); eof != nil {
		rootChildren = append(rootChildren, eof)
	}
	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

func normalizeAuthzedWholeRootErrorTrivia(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || root.symbol != errorSymbol {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}
	quoteSym := Symbol(0)
	if quoteSyms := lang.TokenSymbolsByName("\""); len(quoteSyms) > 0 {
		quoteSym = quoteSyms[0]
	}
	children := authzedRetainedErrorTriviaChildren(source, root.ownerArena, 0, len(source), newlineSym, quoteSym)
	authzedCollapseToSourceFileError(root, source, lang, children)
}

func normalizeAuthzedStrayCaveatTailRecovery(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	children := authzedRecoveryChildren(root, lang)
	caveatIdx := authzedFindDirectChild(children, lang, "caveat_literal", 0)
	if caveatIdx < 0 {
		caveatIdx = authzedFindDirectChildText(children, source, "caveat", 0)
	}
	if caveatIdx < 0 || caveatIdx+5 >= len(children) {
		return
	}
	caveatLiteral := children[caveatIdx]
	name := children[caveatIdx+1]
	params := children[caveatIdx+2]
	lbrace := children[caveatIdx+3]
	left := children[caveatIdx+4]
	exprStmt := children[caveatIdx+5]
	// The recovered left operand may surface as a hidden `_expression`
	// wrapper around the identifier; unwrap it so the rebuilt statement
	// matches the C shape (hidden rules never appear in C trees).
	if symbolTypeName(lang, left.symbol) == "_expression" && resultChildCount(left) == 1 {
		if inner := resultChildAt(left, 0); inner != nil {
			left = inner
		}
	}
	if symbolTypeName(lang, caveatLiteral.symbol) != "caveat_literal" {
		caveatLiteral = authzedLeafByName(root.ownerArena, lang, "caveat_literal", source, int(children[caveatIdx].startByte), int(children[caveatIdx].endByte))
		if caveatLiteral == nil {
			return
		}
	}
	if symbolTypeName(lang, name.symbol) != "identifier" ||
		symbolTypeName(lang, params.symbol) != "parameters_list" ||
		symbolTypeName(lang, lbrace.symbol) != "{" ||
		symbolTypeName(lang, left.symbol) != "identifier" ||
		symbolTypeName(lang, exprStmt.symbol) != "expression_statement" ||
		!exprStmt.hasError() ||
		resultChildCount(exprStmt) != 1 {
		return
	}
	rawBinary := resultChildAt(exprStmt, 0)
	if rawBinary == nil || symbolTypeName(lang, rawBinary.symbol) != "binary_expression" || resultChildCount(rawBinary) < 3 {
		return
	}
	eq := resultChildAt(rawBinary, 0)
	right := resultChildAt(rawBinary, 1)
	rawErr := resultChildAt(rawBinary, 2)
	if eq == nil || right == nil || rawErr == nil ||
		symbolTypeName(lang, eq.symbol) != "==" ||
		symbolTypeName(lang, right.symbol) != "int_literal" ||
		rawErr.symbol != errorSymbol {
		return
	}
	errorStart := int(rawErr.startByte)
	errorEnd := authzedLineEnd(source, errorStart)
	newlineStart := errorEnd
	if errorEnd <= int(right.endByte) || newlineStart >= len(source) || source[errorStart] != '`' || source[newlineStart] != '\n' {
		return
	}
	rbraceStart := newlineStart + 1
	if rbraceStart >= len(source) || source[rbraceStart] != '}' {
		return
	}

	arena := root.ownerArena
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	caveatSym, ok := symbolByName(lang, "caveat")
	if !ok {
		return
	}
	blockSym, ok := symbolByName(lang, "block_c")
	if !ok {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}

	validStmt, ok := authzedCaveatExpressionStatement([]*Node{left, eq, right}, lang, arena)
	if !ok {
		return
	}
	errNode := authzedExtraError(arena, source, errorStart, errorEnd, nil)
	newline := authzedLeaf(arena, lang, newlineSym, false, source, newlineStart, newlineStart+1)
	rbrace := authzedLeafByName(arena, lang, "}", source, rbraceStart, rbraceStart+1)
	if errNode == nil || newline == nil || rbrace == nil {
		return
	}

	block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), cloneNodeSliceInArena(arena, []*Node{lbrace, validStmt, errNode, newline, rbrace}), nil, 0)
	block.setHasError(true)
	authzedSetNodeRange(block, source, int(lbrace.startByte), rbraceStart+1)

	caveat := newParentNodeInArena(arena, caveatSym, symbolIsNamed(lang, caveatSym), cloneNodeSliceInArena(arena, []*Node{caveatLiteral, name, params, block}), authzedCaveatFieldIDs(arena, lang), 0)
	caveat.setHasError(true)
	authzedSetNodeRange(caveat, source, int(caveatLiteral.startByte), rbraceStart+1)

	rootChildren := make([]*Node, 0, caveatIdx+2)
	rootChildren = append(rootChildren, children[:caveatIdx]...)
	rootChildren = append(rootChildren, caveat)
	if eof := authzedEOFLeaf(arena, lang, source); eof != nil {
		rootChildren = append(rootChildren, eof)
	}

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

func normalizeAuthzedObjectCaveatRecovery(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	children := authzedRecoveryChildren(root, lang)
	caveatIdx := authzedFindDirectChild(children, lang, "caveat_literal", 0)
	if caveatIdx < 0 {
		caveatIdx = authzedFindDirectChildText(children, source, "caveat", 0)
	}
	if caveatIdx < 0 || caveatIdx+3 >= len(children) {
		return
	}
	caveatLiteral := children[caveatIdx]
	name := children[caveatIdx+1]
	params := children[caveatIdx+2]
	lbrace := children[caveatIdx+3]
	if symbolTypeName(lang, caveatLiteral.symbol) != "caveat_literal" {
		caveatLiteral = authzedLeafByName(root.ownerArena, lang, "caveat_literal", source, int(children[caveatIdx].startByte), int(children[caveatIdx].endByte))
		if caveatLiteral == nil {
			return
		}
	}
	if symbolTypeName(lang, name.symbol) != "identifier" ||
		symbolTypeName(lang, params.symbol) != "parameters_list" ||
		symbolTypeName(lang, lbrace.symbol) != "{" {
		return
	}

	ampIdx := authzedFindDirectChild(children, lang, "&&", caveatIdx+4)
	if ampIdx < 0 || ampIdx+1 >= len(children) || symbolTypeName(lang, children[ampIdx+1].symbol) != "(" {
		return
	}
	amp := children[ampIdx]
	lparen := children[ampIdx+1]
	errorEnd := authzedLineEnd(source, int(lparen.endByte))
	if errorEnd <= int(lparen.endByte) || bytes.IndexByte(source[int(amp.startByte):errorEnd], '{') < 0 {
		return
	}

	exprStmt, ok := authzedCaveatExpressionStatement(children[caveatIdx+4:ampIdx], lang, root.ownerArena)
	if !ok {
		return
	}

	quoteStart := authzedNextByte(source, errorEnd, '"')
	if quoteStart < 0 {
		return
	}
	quoteEnd := authzedNextByte(source, quoteStart+1, '"')
	if quoteEnd < 0 {
		return
	}
	colonStart := quoteEnd + 1
	if colonStart >= len(source) || source[colonStart] != ':' {
		return
	}
	colonEnd := authzedLineEnd(source, colonStart+1)
	if colonEnd <= colonStart {
		return
	}
	blockClose := authzedNextByte(source, colonEnd, '}')
	if blockClose < 0 {
		return
	}

	arena := root.ownerArena
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	blockSym, ok := symbolByName(lang, "block_c")
	if !ok {
		return
	}
	caveatSym, ok := symbolByName(lang, "caveat")
	if !ok {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}
	quoteSyms := lang.TokenSymbolsByName("\"")
	if len(quoteSyms) == 0 {
		return
	}
	quoteSym := quoteSyms[0]

	firstError := authzedExtraError(arena, source, int(amp.startByte), errorEnd, []*Node{amp, lparen})
	newline1 := authzedLeaf(arena, lang, newlineSym, false, source, errorEnd, errorEnd+1)
	stringStmt := authzedInterpretedStringExpressionStatement(arena, lang, source, quoteStart, quoteEnd+1, quoteSym)
	colon := authzedLeafByName(arena, lang, ":", source, colonStart, colonStart+1)
	secondError := authzedExtraError(arena, source, colonStart, colonEnd, []*Node{colon})
	newline2 := authzedLeaf(arena, lang, newlineSym, false, source, colonEnd, colonEnd+1)
	rbrace := authzedLeafByName(arena, lang, "}", source, blockClose, blockClose+1)
	if firstError == nil || newline1 == nil || stringStmt == nil || colon == nil || secondError == nil || newline2 == nil || rbrace == nil {
		return
	}

	blockChildren := cloneNodeSliceInArena(arena, []*Node{lbrace, exprStmt, firstError, newline1, stringStmt, secondError, newline2, rbrace})
	block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), blockChildren, nil, 0)
	block.setHasError(true)
	authzedSetNodeRange(block, source, int(lbrace.startByte), blockClose+1)

	caveatChildren := cloneNodeSliceInArena(arena, []*Node{caveatLiteral, name, params, block})
	caveat := newParentNodeInArena(arena, caveatSym, symbolIsNamed(lang, caveatSym), caveatChildren, authzedCaveatFieldIDs(arena, lang), 0)
	caveat.setHasError(true)
	authzedSetNodeRange(caveat, source, int(caveatLiteral.startByte), blockClose+1)

	tailOneStart := blockClose + 1
	tailOneEnd := authzedLineEnd(source, tailOneStart)
	if tailOneEnd <= tailOneStart {
		return
	}
	tailOne := authzedExtraError(arena, source, tailOneStart, tailOneEnd, authzedRetainedErrorTriviaChildren(source, arena, tailOneStart, tailOneEnd, newlineSym, quoteSym))
	tailNewline := authzedLeaf(arena, lang, newlineSym, false, source, tailOneEnd, tailOneEnd+1)
	tailTwoStart := tailOneEnd + 1
	tailTwoChildren := make([]*Node, 0, 8)
	if tailTwoStart < len(source) && source[tailTwoStart] == '}' {
		if tailRBrace := authzedLeafByName(arena, lang, "}", source, tailTwoStart, tailTwoStart+1); tailRBrace != nil {
			tailTwoChildren = append(tailTwoChildren, tailRBrace)
			tailTwoStart++
		}
	}
	tailTwoChildren = append(tailTwoChildren, authzedRetainedErrorTriviaChildren(source, arena, tailTwoStart, len(source), newlineSym, quoteSym)...)
	tailTwo := authzedExtraError(arena, source, tailOneEnd+1, len(source), tailTwoChildren)
	if tailOne == nil || tailNewline == nil || tailTwo == nil {
		return
	}

	rootChildren := make([]*Node, 0, caveatIdx+5)
	rootChildren = append(rootChildren, children[:caveatIdx]...)
	rootChildren = append(rootChildren, caveat, tailOne, tailNewline, tailTwo)

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

func normalizeAuthzedSingleQuotedCaveatRecovery(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	children := authzedRecoveryChildren(root, lang)
	caveatIdx := authzedFindDirectChild(children, lang, "caveat_literal", 0)
	if caveatIdx < 0 {
		caveatIdx = authzedFindDirectChildText(children, source, "caveat", 0)
	}
	if caveatIdx < 0 || caveatIdx+3 >= len(children) {
		return
	}
	caveatLiteral := children[caveatIdx]
	if symbolTypeName(lang, caveatLiteral.symbol) != "caveat_literal" {
		caveatLiteral = authzedLeafByName(root.ownerArena, lang, "caveat_literal", source, int(children[caveatIdx].startByte), int(children[caveatIdx].endByte))
		if caveatLiteral == nil {
			return
		}
	}
	name := children[caveatIdx+1]
	params := children[caveatIdx+2]
	lbrace := children[caveatIdx+3]
	if symbolTypeName(lang, name.symbol) != "identifier" ||
		symbolTypeName(lang, params.symbol) != "parameters_list" ||
		symbolTypeName(lang, lbrace.symbol) != "{" {
		return
	}
	eqIdx := authzedFindDirectChild(children, lang, "==", caveatIdx+4)
	if eqIdx < 0 {
		return
	}
	eq := children[eqIdx]
	invalidEnd := authzedLineEnd(source, int(eq.endByte))
	if invalidEnd <= int(eq.endByte) || bytes.IndexByte(source[int(eq.endByte):invalidEnd], '\'') < 0 {
		return
	}
	rbraceStart := authzedNextNonHorizontalSpace(source, invalidEnd)
	if rbraceStart < 0 || rbraceStart >= len(source) || source[rbraceStart] != '}' {
		return
	}
	rbraceEnd := rbraceStart + 1

	exprStmt, ok := authzedCaveatExpressionStatement(children[caveatIdx+4:eqIdx], lang, root.ownerArena)
	if !ok {
		return
	}

	arena := root.ownerArena
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}
	blockSym, ok := symbolByName(lang, "block_c")
	if !ok {
		return
	}
	caveatSym, ok := symbolByName(lang, "caveat")
	if !ok {
		return
	}

	errorNode := newParentNodeInArena(arena, errorSymbol, true, cloneNodeSliceInArena(arena, []*Node{eq}), nil, 0)
	errorNode.setExtra(true)
	errorNode.setHasError(true)
	authzedSetNodeRange(errorNode, source, int(eq.startByte), invalidEnd)

	newline := authzedLeaf(arena, lang, newlineSym, false, source, invalidEnd, invalidEnd+1)
	rbrace := authzedLeafByName(arena, lang, "}", source, rbraceStart, rbraceEnd)
	if newline == nil || rbrace == nil {
		return
	}

	blockChildren := cloneNodeSliceInArena(arena, []*Node{lbrace, exprStmt, errorNode, newline, rbrace})
	block := newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), blockChildren, nil, 0)
	block.setHasError(true)
	authzedSetNodeRange(block, source, int(lbrace.startByte), rbraceEnd)

	caveatFields := authzedCaveatFieldIDs(arena, lang)
	caveatChildren := cloneNodeSliceInArena(arena, []*Node{caveatLiteral, name, params, block})
	caveat := newParentNodeInArena(arena, caveatSym, symbolIsNamed(lang, caveatSym), caveatChildren, caveatFields, 0)
	caveat.setHasError(true)
	authzedSetNodeRange(caveat, source, int(caveatLiteral.startByte), rbraceEnd)

	rootChildren := make([]*Node, 0, caveatIdx+4)
	rootChildren = append(rootChildren, children[:caveatIdx]...)
	rootChildren = append(rootChildren, caveat)
	rootChildren = authzedAppendRootTailFromSource(rootChildren, source, arena, lang, rbraceEnd)

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
}

// normalizeAuthzedSingleQuotedCaveatBlockRecovery handles the structured
// recovery shape for a single-quoted caveat literal: the parser produces a
// proper caveat/block_c subtree whose trailing comparison is a broken
// binary_expression (identifier == <missing>) because the single-quoted
// chunk emits no token. The C oracle instead keeps the statement up to the
// last identifier and wraps the dangling "==" (through the quoted literal)
// in an ERROR node followed by the newline.
func normalizeAuthzedSingleQuotedCaveatBlockRecovery(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	if symbolTypeName(lang, root.symbol) != "source_file" {
		return
	}
	for _, child := range resultChildSliceForMutation(root) {
		if child == nil || symbolTypeName(lang, child.symbol) != "caveat" || !child.hasError() {
			continue
		}
		if authzedRebuildSingleQuotedCaveatBlock(child, source, lang, root.ownerArena) {
			return
		}
	}
}

func authzedRebuildSingleQuotedCaveatBlock(caveat *Node, source []byte, lang *Language, arena *nodeArena) bool {
	n := resultChildCount(caveat)
	if n == 0 {
		return false
	}
	block := resultChildAt(caveat, n-1)
	if block == nil || symbolTypeName(lang, block.symbol) != "block_c" ||
		!block.hasError() || resultChildCount(block) != 3 {
		return false
	}
	lbrace := resultChildAt(block, 0)
	exprStmt := resultChildAt(block, 1)
	rbrace := resultChildAt(block, 2)
	if lbrace == nil || exprStmt == nil || rbrace == nil ||
		symbolTypeName(lang, lbrace.symbol) != "{" ||
		symbolTypeName(lang, exprStmt.symbol) != "expression_statement" ||
		!exprStmt.hasError() ||
		symbolTypeName(lang, rbrace.symbol) != "}" ||
		resultChildCount(exprStmt) != 1 {
		return false
	}
	outer := resultChildAt(exprStmt, 0)
	if outer == nil || symbolTypeName(lang, outer.symbol) != "binary_expression" || !outer.hasError() {
		return false
	}
	// The broken comparison is either the expression itself or its
	// right-most binary_expression child.
	broken := outer
	if resultChildCount(outer) == 3 {
		if last := resultChildAt(outer, 2); last != nil &&
			symbolTypeName(lang, last.symbol) == "binary_expression" && last.hasError() {
			broken = last
		}
	}
	if resultChildCount(broken) != 3 {
		return false
	}
	goodRight := resultChildAt(broken, 0)
	eq := resultChildAt(broken, 1)
	missing := resultChildAt(broken, 2)
	if goodRight == nil || eq == nil || missing == nil ||
		symbolTypeName(lang, goodRight.symbol) != "identifier" ||
		symbolTypeName(lang, eq.symbol) != "==" ||
		missing.startByte != missing.endByte {
		return false
	}
	errorStart := int(eq.startByte)
	errorEnd := authzedLineEnd(source, int(eq.endByte))
	if errorEnd <= int(eq.endByte) || bytes.IndexByte(source[int(eq.endByte):errorEnd], '\'') < 0 {
		return false
	}
	if errorEnd >= len(source) || source[errorEnd] != '\n' {
		return false
	}
	if int(rbrace.startByte) != errorEnd+1 {
		return false
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return false
	}

	expr := goodRight
	if broken != outer {
		expr = newParentNodeInArena(arena, outer.symbol, outer.isNamed(),
			cloneNodeSliceInArena(arena, []*Node{resultChildAt(outer, 0), resultChildAt(outer, 1), goodRight}),
			authzedBinaryExpressionFieldIDs(arena, lang), 0)
	}
	newStmt, ok := authzedCaveatExpressionStatement([]*Node{expr}, lang, arena)
	if !ok {
		return false
	}
	errNode := authzedExtraError(arena, source, errorStart, errorEnd, []*Node{eq})
	newline := authzedLeaf(arena, lang, newlineSym, false, source, errorEnd, errorEnd+1)
	if errNode == nil || newline == nil {
		return false
	}

	block.children = cloneNodeSliceInArena(arena, []*Node{lbrace, newStmt, errNode, newline, rbrace})
	block.clearFieldMetadata()
	populateParentNode(block, block.children)
	block.setHasError(true)
	caveat.setHasError(true)
	return true
}

func normalizeAuthzedUnsupportedUseDirective(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	useStart := authzedUnsupportedUseDirectiveStart(source)
	if useStart < 0 {
		return
	}
	useLineEnd := authzedLineEnd(source, useStart)
	if useLineEnd < 0 {
		useLineEnd = len(source)
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}
	quoteSyms := lang.TokenSymbolsByName("\"")
	if len(quoteSyms) == 0 {
		return
	}
	errorChildren := authzedRetainedErrorTriviaChildren(source, root.ownerArena, useLineEnd, len(source), newlineSym, quoteSyms[0])
	leading := authzedLeadingCommentChildren(root, source, lang, useStart)
	if len(leading) == 0 && useStart > 0 {
		leading = authzedLeadingDefinitionsBeforeUse(source, root.ownerArena, lang, useStart)
	}
	if len(leading) == 0 {
		authzedCollapseToSourceFileError(root, source, lang, errorChildren)
		return
	}
	authzedCollapseToSourceFileWithLeadingError(root, source, lang, leading, useStart, errorChildren)
}

func normalizeAuthzedMalformedDefinitionRoot(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "authzed" || len(source) == 0 {
		return
	}
	if !bytes.HasPrefix(source, []byte("definition ")) {
		return
	}
	newlineSym, ok := symbolByName(lang, "\n")
	if !ok {
		return
	}
	quoteSym := Symbol(0)
	if quoteSyms := lang.TokenSymbolsByName("\""); len(quoteSyms) > 0 {
		quoteSym = quoteSyms[0]
	}
	if root.symbol != errorSymbol {
		if authzedNormalizeDefinitionInvalidRelation(root, source, lang, newlineSym, quoteSym) {
			return
		}
		authzedNormalizeMalformedDefinitionErrorChild(root, source, lang, newlineSym, quoteSym)
		return
	}
	children := authzedMalformedDefinitionErrorChildren(root, source, lang, newlineSym, quoteSym)
	authzedCollapseToSourceFileError(root, source, lang, children)
}

func authzedNormalizeDefinitionInvalidRelation(root *Node, source []byte, lang *Language, newlineSym, quoteSym Symbol) bool {
	if root == nil || symbolTypeName(lang, root.symbol) != "source_file" || resultChildCount(root) == 0 {
		return false
	}
	definition := resultChildAt(root, 0)
	if definition == nil || symbolTypeName(lang, definition.symbol) != "definition" || !definition.hasError() || resultChildCount(definition) < 3 {
		return false
	}
	defLit := resultChildAt(definition, 0)
	name := resultChildAt(definition, 1)
	block := resultChildAt(definition, 2)
	if defLit == nil || name == nil || block == nil || symbolTypeName(lang, block.symbol) != "block" || !block.hasError() || resultChildCount(block) == 0 {
		return false
	}
	lbrace := resultChildAt(block, 0)
	if lbrace == nil || symbolTypeName(lang, lbrace.symbol) != "{" {
		return false
	}
	var comment *Node
	var relation *Node
	for i := 1; i < resultChildCount(block); i++ {
		child := resultChildAt(block, i)
		if child == nil {
			continue
		}
		switch symbolTypeName(lang, child.symbol) {
		case "comment":
			if comment == nil {
				comment = child
			}
		case "relation":
			if child.hasError() {
				relation = child
			}
		}
		if relation != nil {
			break
		}
	}
	if comment == nil || relation == nil {
		return false
	}
	partialRelation, ok := authzedPartialRelationBeforeError(relation, lang, root.ownerArena)
	if !ok {
		return false
	}

	children := make([]*Node, 0, 10)
	children = append(children, defLit, name, lbrace, comment, partialRelation)
	children = append(children, authzedRetainedErrorTriviaChildren(source, root.ownerArena, int(partialRelation.endByte), len(source), newlineSym, quoteSym)...)
	authzedCollapseToSourceFileError(root, source, lang, cloneNodeSliceInArena(root.ownerArena, children))
	return true
}

func authzedPartialRelationBeforeError(relation *Node, lang *Language, arena *nodeArena) (*Node, bool) {
	if relation == nil || resultChildCount(relation) < 3 {
		return nil, false
	}
	relationSym := relation.symbol
	relExpr := resultChildAt(relation, 2)
	if relExpr == nil || symbolTypeName(lang, relExpr.symbol) != "rel_expression" || !relExpr.hasError() {
		return nil, false
	}
	kept := make([]*Node, 0, resultChildCount(relExpr))
	for i := 0; i < resultChildCount(relExpr); i++ {
		child := resultChildAt(relExpr, i)
		if child == nil {
			continue
		}
		if child.symbol == errorSymbol || child.hasError() {
			break
		}
		kept = append(kept, child)
	}
	if len(kept) == 0 {
		return nil, false
	}
	relExprNode := newParentNodeInArena(arena, relExpr.symbol, symbolIsNamed(lang, relExpr.symbol), cloneNodeSliceInArena(arena, kept), nil, 0)
	relationChildren := cloneNodeSliceInArena(arena, []*Node{resultChildAt(relation, 0), resultChildAt(relation, 1), relExprNode})
	partial := newParentNodeInArena(arena, relationSym, symbolIsNamed(lang, relationSym), relationChildren, authzedRelationFieldIDs(arena, lang), 0)
	partial.setHasError(false)
	return partial, true
}

func authzedRelationFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	fields := make([]FieldID, 3)
	if fid, ok := lang.FieldByName("relation"); ok {
		fields[0] = fid
	}
	if fid, ok := lang.FieldByName("relation_name"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("relation_expression"); ok {
		fields[2] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func authzedNormalizeMalformedDefinitionErrorChild(root *Node, source []byte, lang *Language, newlineSym, quoteSym Symbol) {
	if root == nil || symbolTypeName(lang, root.symbol) != "source_file" || resultChildCount(root) != 1 {
		return
	}
	errNode := resultChildAt(root, 0)
	if errNode == nil || errNode.symbol != errorSymbol || !errNode.isExtra() {
		return
	}
	children, changed := authzedNormalizeMalformedDefinitionErrorChildChildren(resultChildSliceForMutation(errNode), source, errNode.ownerArena, lang, newlineSym, quoteSym)
	if !changed {
		return
	}
	errNode.children = cloneNodeSliceInArena(errNode.ownerArena, children)
	errNode.clearFieldMetadata()
	populateParentNode(errNode, errNode.children)
	errNode.startByte = 0
	errNode.endByte = uint32(len(source))
	errNode.startPoint = Point{}
	errNode.endPoint = advancePointByBytes(Point{}, source)
	errNode.setHasError(true)
	root.setHasError(true)
}

func authzedNormalizeMalformedDefinitionErrorChildChildren(children []*Node, source []byte, arena *nodeArena, lang *Language, newlineSym, quoteSym Symbol) ([]*Node, bool) {
	for i := 0; i+1 < len(children); i++ {
		if symbolTypeName(lang, children[i].symbol) != "permission_literal" {
			continue
		}
		permission := children[i+1]
		if permission == nil || symbolTypeName(lang, permission.symbol) != "permission" || !permission.hasError() || resultChildCount(permission) == 0 {
			continue
		}
		name := resultChildAt(permission, 0)
		if name != nil && name.symbol == errorSymbol {
			out := make([]*Node, 0, i+3)
			out = append(out, children[:i+1]...)
			out = append(out, authzedRetainedErrorTriviaChildren(source, arena, int(permission.endByte), len(source), newlineSym, quoteSym)...)
			return cloneNodeSliceInArena(arena, out), true
		}
		if name == nil || symbolTypeName(lang, name.symbol) != "identifier" {
			continue
		}
		out := make([]*Node, 0, i+4)
		out = append(out, children[:i+1]...)
		out = append(out, name)
		out = append(out, authzedRetainedErrorTriviaChildren(source, arena, int(permission.endByte), len(source), newlineSym, quoteSym)...)
		return cloneNodeSliceInArena(arena, out), true
	}
	return children, false
}

func authzedCollapseToSourceFileError(root *Node, source []byte, lang *Language, children []*Node) {
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	arena := root.ownerArena
	errNode := newParentNodeInArena(arena, errorSymbol, true, children, nil, 0)
	errNode.setExtra(true)
	errNode.setHasError(true)
	errNode.startByte = 0
	errNode.endByte = uint32(len(source))
	errNode.startPoint = Point{}
	errNode.endPoint = advancePointByBytes(Point{}, source)

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = errNode.endPoint
	root.children = cloneNodeSliceInArena(arena, []*Node{errNode})
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
}

func authzedCollapseToSourceFileWithLeadingError(root *Node, source []byte, lang *Language, leading []*Node, errorStart int, errorChildren []*Node) {
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	arena := root.ownerArena
	errNode := newParentNodeInArena(arena, errorSymbol, true, errorChildren, nil, 0)
	errNode.setExtra(true)
	errNode.setHasError(true)
	authzedSetNodeRange(errNode, source, errorStart, len(source))

	rootChildren := make([]*Node, 0, len(leading)+1)
	rootChildren = append(rootChildren, leading...)
	rootChildren = append(rootChildren, errNode)

	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.setExtra(false)
	root.setMissing(false)
	root.setHasError(true)
	root.startByte = 0
	root.endByte = uint32(len(source))
	root.startPoint = Point{}
	root.endPoint = advancePointByBytes(Point{}, source)
	root.children = cloneNodeSliceInArena(arena, rootChildren)
	root.clearFieldMetadata()
	populateParentNode(root, root.children)
}

func authzedUnsupportedUseDirectiveStart(source []byte) int {
	for start := 0; start < len(source); {
		lineEnd := authzedLineEnd(source, start)
		if lineEnd < 0 {
			lineEnd = len(source)
		}
		line := bytes.TrimLeft(source[start:lineEnd], " \t")
		if bytes.HasPrefix(line, []byte("use ")) || bytes.HasPrefix(line, []byte("use\t")) {
			return start + (len(source[start:lineEnd]) - len(line))
		}
		if lineEnd >= len(source) {
			break
		}
		start = lineEnd + 1
	}
	return -1
}

func authzedLeadingCommentChildren(root *Node, source []byte, lang *Language, before int) []*Node {
	children := authzedRecoveryChildren(root, lang)
	out := make([]*Node, 0, len(children))
	for _, child := range children {
		if child == nil || int(child.startByte) >= before {
			continue
		}
		if symbolTypeName(lang, child.symbol) == "comment" {
			out = append(out, child)
		}
	}
	return cloneNodeSliceInArena(root.ownerArena, out)
}

func authzedLeadingDefinitionsBeforeUse(source []byte, arena *nodeArena, lang *Language, before int) []*Node {
	children := make([]*Node, 0, 4)
	pos := 0
	newlineSym, hasNewline := symbolByName(lang, "\n")
	for pos < before {
		for pos < before {
			switch source[pos] {
			case ' ', '\t', '\r':
				pos++
			default:
				goto content
			}
		}
	content:
		if pos >= before {
			break
		}
		if source[pos] == '\n' {
			pos++
			continue
		}
		if !bytes.HasPrefix(source[pos:], []byte("definition ")) {
			break
		}
		def, next, ok := authzedSimpleDefinitionFromSource(source, arena, lang, pos)
		if !ok || next > before {
			break
		}
		children = append(children, def)
		pos = next
		if hasNewline && pos < before && source[pos] == '\n' {
			children = append(children, authzedLeaf(arena, lang, newlineSym, false, source, pos, pos+1))
			pos++
		}
	}
	return cloneNodeSliceInArena(arena, children)
}

func authzedMalformedDefinitionErrorChildren(root *Node, source []byte, lang *Language, newlineSym, quoteSym Symbol) []*Node {
	if root == nil {
		return nil
	}
	children := make([]*Node, 0, resultChildCount(root))
	for i := 0; i < resultChildCount(root); i++ {
		child := resultChildAt(root, i)
		if child == nil {
			continue
		}
		if child.symbol == errorSymbol {
			if child.isExtra() && resultChildCount(child) > 0 {
				for j := 0; j < resultChildCount(child); j++ {
					grandchild := resultChildAt(child, j)
					if grandchild != nil {
						children = append(children, grandchild)
					}
				}
				continue
			}
			children = append(children, authzedRetainedErrorTriviaChildren(source, root.ownerArena, int(child.startByte), int(child.endByte), newlineSym, quoteSym)...)
			continue
		}
		children = append(children, child)
	}
	children = authzedCoalescePartialPermissionBeforeLineError(children, source, root.ownerArena, lang, newlineSym, quoteSym)
	if normalized, changed := authzedNormalizeMalformedDefinitionErrorChildChildren(children, source, root.ownerArena, lang, newlineSym, quoteSym); changed {
		children = normalized
	}
	return cloneNodeSliceInArena(root.ownerArena, children)
}

func authzedCoalescePartialPermissionBeforeLineError(children []*Node, source []byte, arena *nodeArena, lang *Language, newlineSym, quoteSym Symbol) []*Node {
	if len(children) < 7 || lang == nil {
		return children
	}
	permissionSym, ok := symbolByName(lang, "permission")
	if !ok {
		return children
	}
	permExpressionSym, ok := symbolByName(lang, "perm_expression")
	if !ok {
		return children
	}
	if symbolTypeName(lang, children[3].symbol) != "permission_literal" ||
		symbolTypeName(lang, children[4].symbol) != "identifier" ||
		symbolTypeName(lang, children[5].symbol) != "=" ||
		symbolTypeName(lang, children[6].symbol) != "identifier" {
		return children
	}
	if !authzedHasNewlineBeforeNextRetainedChild(source, children, 6) {
		return children
	}

	exprIdent := children[6]
	permExprChildren := cloneNodeSliceInArena(arena, []*Node{exprIdent})
	permExpr := newParentNodeInArena(arena, permExpressionSym, symbolIsNamed(lang, permExpressionSym), permExprChildren, nil, 0)
	permissionChildren := cloneNodeSliceInArena(arena, []*Node{children[3], children[4], children[5], permExpr})
	permission := newParentNodeInArena(arena, permissionSym, symbolIsNamed(lang, permissionSym), permissionChildren, authzedPermissionFieldIDs(arena, lang), 0)

	out := make([]*Node, 0, 4)
	out = append(out, children[:3]...)
	out = append(out, permission)
	out = append(out, authzedRetainedErrorTriviaChildren(source, arena, int(exprIdent.endByte), len(source), newlineSym, quoteSym)...)
	return cloneNodeSliceInArena(arena, out)
}

func authzedPermissionFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	fields := make([]FieldID, 4)
	if fid, ok := lang.FieldByName("permission"); ok {
		fields[0] = fid
	}
	if fid, ok := lang.FieldByName("param_name"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("permission_expresssion"); ok {
		fields[3] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func authzedFindDirectChild(children []*Node, lang *Language, typ string, start int) int {
	for i := start; i < len(children); i++ {
		if children[i] != nil && symbolTypeName(lang, children[i].symbol) == typ {
			return i
		}
	}
	return -1
}

func authzedRecoveryChildren(root *Node, lang *Language) []*Node {
	children := resultChildSliceForMutation(root)
	if root != nil && symbolTypeName(lang, root.symbol) == "source_file" && resultChildCount(root) == 1 {
		errRoot := resultChildAt(root, 0)
		if errRoot != nil && errRoot.symbol == errorSymbol && errRoot.isExtra() {
			children = resultChildSliceForMutation(errRoot)
		}
	}
	return children
}

func authzedFindDirectChildText(children []*Node, source []byte, text string, start int) int {
	for i := start; i < len(children); i++ {
		child := children[i]
		if child == nil {
			continue
		}
		if int(child.startByte) < 0 || int(child.endByte) > len(source) || child.endByte < child.startByte {
			continue
		}
		if string(source[child.startByte:child.endByte]) == text {
			return i
		}
	}
	return -1
}

func authzedNextByte(source []byte, start int, b byte) int {
	if start < 0 {
		start = 0
	}
	if start >= len(source) {
		return -1
	}
	if idx := bytes.IndexByte(source[start:], b); idx >= 0 {
		return start + idx
	}
	return -1
}

func authzedLineEnd(source []byte, start int) int {
	if start < 0 || start > len(source) {
		return -1
	}
	if idx := bytes.IndexByte(source[start:], '\n'); idx >= 0 {
		return start + idx
	}
	return -1
}

func authzedNextNonHorizontalSpace(source []byte, start int) int {
	if start < 0 || start > len(source) {
		return -1
	}
	for i := start; i < len(source); i++ {
		switch source[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return i
		}
	}
	return -1
}

func authzedExtraError(arena *nodeArena, source []byte, start, end int, children []*Node) *Node {
	if start < 0 || end < start || end > len(source) {
		return nil
	}
	errNode := newParentNodeInArena(arena, errorSymbol, true, cloneNodeSliceInArena(arena, children), nil, 0)
	errNode.setExtra(true)
	errNode.setHasError(true)
	authzedSetNodeRange(errNode, source, start, end)
	return errNode
}

func authzedCaveatExpressionStatement(children []*Node, lang *Language, arena *nodeArena) (*Node, bool) {
	if len(children) == 0 {
		return nil, false
	}
	exprStmtSym, ok := symbolByName(lang, "expression_statement")
	if !ok {
		return nil, false
	}
	expr := children[0]
	if len(children) == 3 && authzedIsBinaryOperator(lang, children[1]) {
		binarySym, ok := symbolByName(lang, "binary_expression")
		if !ok {
			return nil, false
		}
		expr = newParentNodeInArena(arena, binarySym, symbolIsNamed(lang, binarySym), cloneNodeSliceInArena(arena, children), authzedBinaryExpressionFieldIDs(arena, lang), 0)
	}
	if expr == nil {
		return nil, false
	}
	stmt := newParentNodeInArena(arena, exprStmtSym, symbolIsNamed(lang, exprStmtSym), cloneNodeSliceInArena(arena, []*Node{expr}), nil, 0)
	return stmt, true
}

func authzedInterpretedStringExpressionStatement(arena *nodeArena, lang *Language, source []byte, start, end int, quoteSym Symbol) *Node {
	if start < 0 || end > len(source) || end <= start || quoteSym == 0 || source[start] != '"' || source[end-1] != '"' {
		return nil
	}
	stringSym, ok := symbolByName(lang, "interpreted_string_literal")
	if !ok {
		return nil
	}
	openQuote := authzedLeaf(arena, lang, quoteSym, false, source, start, start+1)
	closeQuote := authzedLeaf(arena, lang, quoteSym, false, source, end-1, end)
	if openQuote == nil || closeQuote == nil {
		return nil
	}
	strNode := newParentNodeInArena(arena, stringSym, symbolIsNamed(lang, stringSym), cloneNodeSliceInArena(arena, []*Node{openQuote, closeQuote}), nil, 0)
	authzedSetNodeRange(strNode, source, start, end)
	stmt, ok := authzedCaveatExpressionStatement([]*Node{strNode}, lang, arena)
	if !ok {
		return nil
	}
	return stmt
}

func authzedIsBinaryOperator(lang *Language, n *Node) bool {
	if n == nil {
		return false
	}
	switch symbolTypeName(lang, n.symbol) {
	case "&&", "||", "==", "!=", "<", "<=", ">", ">=", "+", "-", "*", "/", "%", "&", "|", "^", "<<", ">>", "&^":
		return true
	default:
		return false
	}
}

func authzedBinaryExpressionFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	fields := make([]FieldID, 3)
	if fid, ok := lang.FieldByName("left"); ok {
		fields[0] = fid
	}
	if fid, ok := lang.FieldByName("operator"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("right"); ok {
		fields[2] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func authzedCaveatFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	fields := make([]FieldID, 4)
	if fid, ok := lang.FieldByName("name"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("type_parameters"); ok {
		fields[2] = fid
	}
	if fid, ok := lang.FieldByName("body"); ok {
		fields[3] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func authzedDefinitionFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	fields := make([]FieldID, 3)
	if fid, ok := lang.FieldByName("name"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("body"); ok {
		fields[2] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func authzedAppendRootTailFromSource(children []*Node, source []byte, arena *nodeArena, lang *Language, start int) []*Node {
	pos := start
	if newlineSym, ok := symbolByName(lang, "\n"); ok && pos < len(source) && source[pos] == '\n' {
		children = append(children, authzedLeaf(arena, lang, newlineSym, false, source, pos, pos+1))
		pos++
	}
	for pos < len(source) {
		switch source[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			goto tailContent
		}
	}

tailContent:
	for pos < len(source) {
		idx := bytes.Index(source[pos:], []byte("definition "))
		if idx < 0 {
			break
		}
		defStart := pos + idx
		def, next, ok := authzedSimpleDefinitionFromSource(source, arena, lang, defStart)
		if !ok {
			break
		}
		children = append(children, def)
		pos = next
		if newlineSym, ok := symbolByName(lang, "\n"); ok && pos < len(source) && source[pos] == '\n' && bytes.Contains(source[pos+1:], []byte("definition ")) {
			children = append(children, authzedLeaf(arena, lang, newlineSym, false, source, pos, pos+1))
			pos++
		}
	}
	if len(source) == 0 || source[len(source)-1] != '\n' {
		if eof := authzedEOFLeaf(arena, lang, source); eof != nil {
			children = append(children, eof)
		}
	}
	return children
}

func authzedSimpleDefinitionFromSource(source []byte, arena *nodeArena, lang *Language, start int) (*Node, int, bool) {
	if start < 0 || start+len("definition ") > len(source) || !bytes.HasPrefix(source[start:], []byte("definition ")) {
		return nil, start, false
	}
	defSym, ok := symbolByName(lang, "definition")
	if !ok {
		return nil, start, false
	}
	defLit := authzedLeafByName(arena, lang, "definition_literal", source, start, start+len("definition"))
	if defLit == nil {
		return nil, start, false
	}
	nameStart := start + len("definition ")
	nameEnd := nameStart
	for nameEnd < len(source) {
		switch source[nameEnd] {
		case ' ', '\t', '\r', '\n', '{':
			goto foundNameEnd
		default:
			nameEnd++
		}
	}
foundNameEnd:
	if nameEnd <= nameStart {
		return nil, start, false
	}
	name := authzedLeafByName(arena, lang, "identifier", source, nameStart, nameEnd)
	if name == nil {
		return nil, start, false
	}
	braceStartRel := bytes.IndexByte(source[nameEnd:], '{')
	if braceStartRel < 0 {
		return nil, start, false
	}
	braceStart := nameEnd + braceStartRel
	braceEndRel := bytes.IndexByte(source[braceStart+1:], '}')
	if braceEndRel < 0 {
		return nil, start, false
	}
	braceEnd := braceStart + 1 + braceEndRel
	block := authzedSimpleBlockFromSource(source, arena, lang, braceStart, braceEnd+1)
	if block == nil {
		return nil, start, false
	}
	def := newParentNodeInArena(arena, defSym, symbolIsNamed(lang, defSym), cloneNodeSliceInArena(arena, []*Node{defLit, name, block}), authzedDefinitionFieldIDs(arena, lang), 0)
	return def, braceEnd + 1, true
}

func authzedSimpleBlockFromSource(source []byte, arena *nodeArena, lang *Language, start, end int) *Node {
	blockSym, ok := symbolByName(lang, "block")
	if !ok || start < 0 || end > len(source) || end <= start || source[start] != '{' || source[end-1] != '}' {
		return nil
	}
	lbrace := authzedLeafByName(arena, lang, "{", source, start, start+1)
	rbrace := authzedLeafByName(arena, lang, "}", source, end-1, end)
	if lbrace == nil || rbrace == nil {
		return nil
	}
	return newParentNodeInArena(arena, blockSym, symbolIsNamed(lang, blockSym), cloneNodeSliceInArena(arena, []*Node{lbrace, rbrace}), nil, 0)
}

func authzedLeafByName(arena *nodeArena, lang *Language, name string, source []byte, start, end int) *Node {
	sym, ok := symbolByName(lang, name)
	if !ok {
		return nil
	}
	return authzedLeaf(arena, lang, sym, symbolIsNamed(lang, sym), source, start, end)
}

func authzedEOFLeaf(arena *nodeArena, lang *Language, source []byte) *Node {
	var sym Symbol
	if syms := lang.TokenSymbolsByName("\\0"); len(syms) > 0 {
		sym = syms[0]
	} else if syms := lang.TokenSymbolsByName("\x00"); len(syms) > 0 {
		sym = syms[0]
	}
	if sym == 0 {
		return nil
	}
	return authzedLeaf(arena, lang, sym, symbolIsNamed(lang, sym), source, len(source), len(source))
}

func authzedLeaf(arena *nodeArena, lang *Language, sym Symbol, named bool, source []byte, start, end int) *Node {
	if start < 0 || end < start || end > len(source) {
		return nil
	}
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	return newLeafNodeInArena(arena, sym, named, uint32(start), uint32(end), startPoint, endPoint)
}

func authzedSetNodeRange(n *Node, source []byte, start, end int) {
	if n == nil || start < 0 || end < start || end > len(source) {
		return
	}
	n.startByte = uint32(start)
	n.endByte = uint32(end)
	n.startPoint = advancePointByBytes(Point{}, source[:start])
	n.endPoint = advancePointByBytes(n.startPoint, source[start:end])
}

func authzedHasNewlineBeforeNextRetainedChild(source []byte, children []*Node, childIndex int) bool {
	if childIndex < 0 || childIndex >= len(children) || children[childIndex] == nil {
		return false
	}
	start := int(children[childIndex].endByte)
	end := len(source)
	if next := childIndex + 1; next < len(children) && children[next] != nil {
		end = int(children[next].startByte)
	}
	if start < 0 || start > len(source) {
		return false
	}
	if end < start {
		return false
	}
	if end > len(source) {
		end = len(source)
	}
	return bytes.IndexByte(source[start:end], '\n') >= 0
}

func authzedRetainedErrorTriviaChildren(source []byte, arena *nodeArena, start, end int, newlineSym, quoteSym Symbol) []*Node {
	if start < 0 {
		start = 0
	}
	if end > len(source) {
		end = len(source)
	}
	if start > end {
		start = end
	}
	children := make([]*Node, 0, bytes.Count(source[start:end], []byte{'\n'})+bytes.Count(source[start:end], []byte{'"'}))
	point := Point{}
	if start > 0 {
		point = advancePointByBytes(point, source[:start])
	}
	for i := start; i < end; i++ {
		b := source[i]
		leafStart := point
		childEnd := advancePointByBytes(leafStart, source[i:i+1])
		switch b {
		case '\n':
			children = append(children, newLeafNodeInArena(arena, newlineSym, false, uint32(i), uint32(i+1), leafStart, childEnd))
		case '"':
			if quoteSym != 0 {
				children = append(children, newLeafNodeInArena(arena, quoteSym, false, uint32(i), uint32(i+1), leafStart, childEnd))
			}
		}
		point = childEnd
	}
	return cloneNodeSliceInArena(arena, children)
}
