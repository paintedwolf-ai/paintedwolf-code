package gotreesitter

func normalizePHPCompatibility(root *Node, source []byte, parser *Parser, lang *Language) {
	normalizePHPSingletonTypeWrappers(root, lang)
	normalizePHPListLiteralDestructuringTargets(root, lang)
	normalizePHPStaticFunctionFragments(root, source, parser, lang)
}

// normalizePHPListLiteralDestructuringTargets retypes array_creation_expression
// nodes that sit in destructuring-target positions into list_literal, matching
// tree-sitter-c.
//
// In the C grammar the assignment_expression `left` field and the foreach value
// position only accept `_variable | list_literal`; an array literal there is
// always parsed as `_array_destructing` (aliased `list_literal`) with its
// elements as bare children. Go's GLR keeps the array_creation_expression /
// array_element_initializer interpretation for some of these inputs (notably
// `key => value` and pair-valued foreach elements), so retype + unwrap to match
// C byte-for-byte.
func normalizePHPListLiteralDestructuringTargets(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "php" {
		return
	}
	arraySym, arrayOK := symbolByName(lang, "array_creation_expression")
	listSym, listNamed, listOK := symbolMeta(lang, "list_literal")
	initSym, initOK := symbolByName(lang, "array_element_initializer")
	asSym, asOK := phpAnonymousSymbol(lang, "as")
	if !arrayOK || !listOK || !initOK || !asOK {
		return
	}

	retype := func(arr *Node) {
		if arr == nil || arr.symbol != arraySym {
			return
		}
		phpRetypeArrayToListLiteral(arr, listSym, listNamed, initSym, lang)
	}

	walkResultTree(root, func(n *Node) {
		switch n.Type(lang) {
		case "assignment_expression":
			retype(phpAssignmentLeft(n, lang))
		case "foreach_statement":
			value := phpForeachValueChild(n, asSym)
			if value == nil {
				return
			}
			if value.Type(lang) == "pair" {
				retype(phpPairValue(value))
				return
			}
			retype(value)
		}
	})
}

// phpAssignmentLeft returns the child of an assignment_expression carrying the
// `left` field.
func phpAssignmentLeft(n *Node, lang *Language) *Node {
	if n == nil {
		return nil
	}
	leftField, ok := lang.FieldByName("left")
	if !ok {
		return nil
	}
	count := resultChildCount(n)
	children := resultChildSliceForMutation(n)
	fieldIDs := n.fieldIDs()
	for i := 0; i < count && i < len(fieldIDs); i++ {
		if fieldIDs[i] == leftField && i < len(children) {
			return children[i]
		}
	}
	return nil
}

// phpForeachValueChild returns the named child of a foreach_statement that holds
// the loop value, i.e. the first named child following the anonymous `as` token.
func phpForeachValueChild(n *Node, asSym Symbol) *Node {
	if n == nil {
		return nil
	}
	count := resultChildCount(n)
	sawAs := false
	for i := 0; i < count; i++ {
		child := resultChildAt(n, i)
		if child == nil {
			continue
		}
		if !sawAs {
			if child.symbol == asSym {
				sawAs = true
			}
			continue
		}
		if child.IsNamed() {
			return child
		}
	}
	return nil
}

// phpPairValue returns the value side (last named child) of a foreach `pair`.
func phpPairValue(pair *Node) *Node {
	if pair == nil {
		return nil
	}
	count := resultChildCount(pair)
	for i := count - 1; i >= 0; i-- {
		child := resultChildAt(pair, i)
		if child != nil && child.IsNamed() {
			return child
		}
	}
	return nil
}

// phpRetypeArrayToListLiteral converts an array_creation_expression node into a
// list_literal in place, unwrapping each array_element_initializer child so its
// inner nodes become direct children (matching C's _array_destructing shape).
func phpRetypeArrayToListLiteral(arr *Node, listSym Symbol, listNamed bool, initSym Symbol, lang *Language) {
	if arr == nil {
		return
	}
	children := resultChildSliceForMutation(arr)
	out := make([]*Node, 0, len(children))
	unwrapped := false
	for _, child := range children {
		if child == nil {
			out = append(out, child)
			continue
		}
		if child.symbol == initSym {
			inner := resultChildSliceForMutation(child)
			out = append(out, inner...)
			unwrapped = true
			continue
		}
		out = append(out, child)
	}
	arr.symbol = listSym
	arr.setNamed(listNamed)
	if unwrapped {
		replaceNodeChildrenUnfielded(arr, cloneNodeSliceInArena(arr.ownerArena, out))
	}
}

func phpAnonymousSymbol(lang *Language, name string) (Symbol, bool) {
	sym, ok := lang.symbolByNameAndNamed(name, false)
	if ok {
		return sym, true
	}
	return symbolByName(lang, name)
}

func normalizePHPSingletonTypeWrappers(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "php" {
		return
	}
	walkResultTree(root, func(n *Node) {
		for i, child := range n.children {
			if child == nil {
				continue
			}
			switch child.Type(lang) {
			case "intersection_type", "union_type":
				if len(child.children) == 1 && child.children[0] != nil && child.children[0].IsNamed() {
					n.children[i] = child.children[0]
				}
			}
		}
	})
}

func normalizePHPStaticFunctionFragments(root *Node, source []byte, parser *Parser, lang *Language) {
	if root == nil || lang == nil || lang.Name != "php" || len(root.children) == 0 {
		return
	}
	rootType := root.Type(lang)
	if rootType != "program" && rootType != "ERROR" {
		return
	}
	children := root.children
	changed := false
	if children[0] != nil && ((rootType == "program" && children[0].Type(lang) == rootType) || (rootType == "ERROR" && children[0].Type(lang) == "program")) {
		flat := make([]*Node, 0, len(children[0].children)+len(children)-1)
		flat = append(flat, children[0].children...)
		flat = append(flat, children[1:]...)
		children = flat
		changed = true
	}
	arena := root.ownerArena
	out := make([]*Node, 0, len(children))
	seenNonExtra := false
	for i := 0; i < len(children); {
		if repl, consumed, ok := rewritePHPStaticAnonymousHeaderWithTrailingArrowFragments(children[i:], source, parser, lang, arena); ok {
			out = append(out, repl...)
			i += consumed
			changed = true
			for _, n := range repl {
				if phpCountsAsPriorTopLevelNode(n, lang) {
					seenNonExtra = true
				}
			}
			continue
		}
		if repl, consumed, ok := rewritePHPStaticNamedFunctionFragmentsWithTrailingMalformedSibling(children[i:], source, parser, lang, arena, seenNonExtra); ok {
			out = append(out, repl...)
			i += consumed
			changed = true
			for _, n := range repl {
				if phpCountsAsPriorTopLevelNode(n, lang) {
					seenNonExtra = true
				}
			}
			continue
		}
		if repl, consumed, ok := rewritePHPStaticNamedFunctionFragments(children[i:], source, parser, lang, arena, seenNonExtra); ok {
			out = append(out, repl...)
			i += consumed
			changed = true
			for _, n := range repl {
				if phpCountsAsPriorTopLevelNode(n, lang) {
					seenNonExtra = true
				}
			}
			continue
		}
		if repl, consumed, ok := rewritePHPStaticAnonymousFunctionFragments(children[i:], source, lang, arena); ok {
			out = append(out, repl...)
			i += consumed
			changed = true
			for _, n := range repl {
				if phpCountsAsPriorTopLevelNode(n, lang) {
					seenNonExtra = true
				}
			}
			continue
		}
		out = append(out, children[i])
		if phpCountsAsPriorTopLevelNode(children[i], lang) {
			seenNonExtra = true
		}
		i++
	}
	if !changed {
		return
	}
	out = cloneNodeSliceIfArena(arena, out)
	root.children = out
	root.clearFieldMetadata()
	assignPHPTopLevelFragmentFields(root, lang, arena)
	populateParentNode(root, out)
	extendNodeToTrailingWhitespace(root, source)
}

func rewritePHPStaticAnonymousHeaderWithTrailingArrowFragments(nodes []*Node, source []byte, parser *Parser, lang *Language, arena *nodeArena) ([]*Node, int, bool) {
	if len(nodes) < 4 {
		return nil, 0, false
	}
	headerErr := nodes[0]
	openBrace := nodes[1]
	body := nodes[2]
	arrowStmt := nodes[3]
	if headerErr == nil || openBrace == nil || body == nil || arrowStmt == nil {
		return nil, 0, false
	}
	if headerErr.Type(lang) != "ERROR" || len(headerErr.children) != 1 || headerErr.children[0] == nil || headerErr.children[0].Type(lang) != "_anonymous_function_header" {
		return nil, 0, false
	}
	header := headerErr.children[0]
	if len(header.children) != 3 || header.children[0] == nil || header.children[1] == nil || header.children[2] == nil {
		return nil, 0, false
	}
	if header.children[0].Type(lang) != "static_modifier" || header.children[1].Type(lang) != "function" || header.children[2].Type(lang) != "formal_parameters" {
		return nil, 0, false
	}
	if openBrace.Type(lang) != "{" || body.Type(lang) != "compound_statement" || len(body.children) < 2 {
		return nil, 0, false
	}
	closeBrace := body.children[0]
	if closeBrace == nil || closeBrace.Type(lang) != "}" {
		return nil, 0, false
	}
	var trailingComment *Node
	var suffixStart uint32
	switch {
	case len(body.children) >= 3 && body.children[1] != nil && body.children[1].Type(lang) == "comment" && body.children[2] != nil:
		trailingComment = body.children[1]
		suffixStart = body.children[2].startByte
	case len(body.children) >= 2 && body.children[1] != nil:
		suffixStart = body.children[1].startByte
	default:
		return nil, 0, false
	}
	if arrowStmt.Type(lang) != "statement" || suffixStart == 0 || int(suffixStart) >= len(source) {
		return nil, 0, false
	}

	closeErrChildren := phpAllocChildren(arena, 1)
	closeErrChildren[0] = closeBrace
	closeErr := newParentNodeInArena(arena, errorSymbol, true, closeErrChildren, nil, 0)
	closeErr.setHasError(true)
	closeErr.setExtra(true)

	prefixLen := 5
	if trailingComment != nil {
		prefixLen++
	}
	prefix := phpAllocChildren(arena, prefixLen)
	prefix[0] = header.children[0]
	prefix[1] = header.children[1]
	prefix[2] = header.children[2]
	prefix[3] = openBrace
	prefix[4] = closeErr
	if trailingComment != nil {
		prefix[5] = trailingComment
	}

	suffix, ok := phpReparsedTopLevelSuffix(source, suffixStart, parser, lang, arena)
	if !ok {
		return nil, 0, false
	}
	combined := phpAllocChildren(arena, len(prefix)+len(suffix))
	copy(combined, prefix)
	copy(combined[len(prefix):], suffix)
	return combined, len(nodes), true
}

func rewritePHPStaticNamedFunctionFragments(nodes []*Node, source []byte, parser *Parser, lang *Language, arena *nodeArena, hasPriorNonExtra bool) ([]*Node, int, bool) {
	if len(nodes) < 3 {
		return nil, 0, false
	}
	staticErr := nodes[0]
	header := nodes[1]
	bodyErr := nodes[2]
	if staticErr == nil || header == nil || bodyErr == nil {
		return nil, 0, false
	}
	if staticErr.Type(lang) != "ERROR" || len(staticErr.children) != 1 || staticErr.children[0] == nil || staticErr.children[0].Type(lang) != "static_modifier" {
		return nil, 0, false
	}
	if header.Type(lang) != "_anonymous_function_header" || len(header.children) != 3 {
		return nil, 0, false
	}
	if header.children[0] == nil || header.children[0].Type(lang) != "function" {
		return nil, 0, false
	}
	if header.children[1] == nil || header.children[1].Type(lang) != "ERROR" {
		return nil, 0, false
	}
	if header.children[2] == nil || header.children[2].Type(lang) != "formal_parameters" {
		return nil, 0, false
	}
	body, ok := phpSyntheticCompoundStatementFromError(bodyErr, source, lang, arena)
	if !ok {
		return nil, 0, false
	}
	nameNode, ok := phpSyntheticNamedFunctionName(header.children[1], lang, arena)
	if !ok {
		return nil, 0, false
	}
	args, ok := phpSyntheticArgumentsFromFormals(header.children[2], lang, arena)
	if !ok {
		return nil, 0, false
	}
	callSym, callNamed, ok := symbolMeta(lang, "function_call_expression")
	if !ok {
		return nil, 0, false
	}
	callChildren := phpAllocChildren(arena, 2)
	callChildren[0] = nameNode
	callChildren[1] = args
	call := newParentNodeInArena(arena, callSym, callNamed, callChildren, phpSyntheticFieldIDs(arena, 2, lang, map[int]string{
		0: "function",
		1: "arguments",
	}), 0)

	errChildren := phpAllocChildren(arena, 3)
	errChildren[0] = staticErr.children[0]
	errChildren[1] = header.children[0]
	errChildren[2] = call
	if hasPriorNonExtra {
		errChildren = errChildren[:2]
		errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
		errNode.setHasError(true)
		errNode.setExtra(true)

		semiSym, ok := lang.SymbolByName(";")
		if !ok {
			return nil, 0, false
		}
		semi := newLeafNodeInArena(arena, semiSym, false, call.endByte, call.endByte, call.endPoint, call.endPoint)
		// Zero-width recovery-synthesized ";" (no source text): tree-sitter-c
		// marks such inserted terminals Missing, not merely error. Match it so
		// php error trees are IsMissing-clean against the C oracle.
		semi.setMissing(true)
		semi.setHasError(true)

		exprSym, exprNamed, ok := symbolMeta(lang, "expression_statement")
		if !ok {
			return nil, 0, false
		}
		exprChildren := phpAllocChildren(arena, 2)
		exprChildren[0] = call
		exprChildren[1] = semi
		expr := newParentNodeInArena(arena, exprSym, exprNamed, exprChildren, nil, 0)

		repl := phpAllocChildren(arena, 3)
		repl[0] = errNode
		repl[1] = expr
		repl[2] = body
		if suffix, ok := phpReparsedTopLevelSuffix(source, body.endByte, parser, lang, arena); ok {
			combined := phpAllocChildren(arena, len(repl)+len(suffix))
			copy(combined, repl)
			copy(combined[len(repl):], suffix)
			return combined, len(nodes), true
		}
		return repl, 3, true
	}

	errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
	errNode.setHasError(true)
	errNode.setExtra(true)

	repl := phpAllocChildren(arena, 2)
	repl[0] = errNode
	repl[1] = body
	if suffix, ok := phpReparsedTopLevelSuffix(source, body.endByte, parser, lang, arena); ok {
		combined := phpAllocChildren(arena, len(repl)+len(suffix))
		copy(combined, repl)
		copy(combined[len(repl):], suffix)
		return combined, len(nodes), true
	}
	return repl, 3, true
}

func rewritePHPStaticNamedFunctionFragmentsWithTrailingMalformedSibling(nodes []*Node, source []byte, parser *Parser, lang *Language, arena *nodeArena, hasPriorNonExtra bool) ([]*Node, int, bool) {
	if len(nodes) < 3 {
		return nil, 0, false
	}
	staticErr := nodes[0]
	header := nodes[1]
	bodyCarrier := nodes[2]
	if staticErr == nil || header == nil || bodyCarrier == nil {
		return nil, 0, false
	}
	if staticErr.Type(lang) != "ERROR" || len(staticErr.children) != 1 || staticErr.children[0] == nil || staticErr.children[0].Type(lang) != "static_modifier" {
		return nil, 0, false
	}
	if header.Type(lang) != "_anonymous_function_header" || len(header.children) != 3 {
		return nil, 0, false
	}
	if header.children[0] == nil || header.children[0].Type(lang) != "function" {
		return nil, 0, false
	}
	if header.children[1] == nil || header.children[1].Type(lang) != "ERROR" {
		return nil, 0, false
	}
	if header.children[2] == nil || header.children[2].Type(lang) != "formal_parameters" {
		return nil, 0, false
	}
	if bodyCarrier.Type(lang) != "_anonymous_function_header" && bodyCarrier.Type(lang) != "_arrow_function_header" {
		return nil, 0, false
	}
	if len(bodyCarrier.children) == 0 || bodyCarrier.children[0] == nil || bodyCarrier.children[0].Type(lang) != "ERROR" {
		return nil, 0, false
	}
	body, ok := phpSyntheticCompoundStatementFromError(bodyCarrier.children[0], source, lang, arena)
	if !ok {
		return nil, 0, false
	}
	nameNode, ok := phpSyntheticNamedFunctionName(header.children[1], lang, arena)
	if !ok {
		return nil, 0, false
	}
	args, ok := phpSyntheticArgumentsFromFormals(header.children[2], lang, arena)
	if !ok {
		return nil, 0, false
	}
	callSym, callNamed, ok := symbolMeta(lang, "function_call_expression")
	if !ok {
		return nil, 0, false
	}
	callChildren := phpAllocChildren(arena, 2)
	callChildren[0] = nameNode
	callChildren[1] = args
	call := newParentNodeInArena(arena, callSym, callNamed, callChildren, phpSyntheticFieldIDs(arena, 2, lang, map[int]string{
		0: "function",
		1: "arguments",
	}), 0)

	errChildren := phpAllocChildren(arena, 3)
	errChildren[0] = staticErr.children[0]
	errChildren[1] = header.children[0]
	errChildren[2] = call
	var repl []*Node
	if hasPriorNonExtra {
		errChildren = errChildren[:2]
		errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
		errNode.setHasError(true)
		errNode.setExtra(true)

		semiSym, ok := lang.SymbolByName(";")
		if !ok {
			return nil, 0, false
		}
		semi := newLeafNodeInArena(arena, semiSym, false, call.endByte, call.endByte, call.endPoint, call.endPoint)
		// Zero-width recovery-synthesized ";" (no source text): tree-sitter-c
		// marks such inserted terminals Missing, not merely error. Match it so
		// php error trees are IsMissing-clean against the C oracle.
		semi.setMissing(true)
		semi.setHasError(true)

		exprSym, exprNamed, ok := symbolMeta(lang, "expression_statement")
		if !ok {
			return nil, 0, false
		}
		exprChildren := phpAllocChildren(arena, 2)
		exprChildren[0] = call
		exprChildren[1] = semi
		expr := newParentNodeInArena(arena, exprSym, exprNamed, exprChildren, nil, 0)

		repl = phpAllocChildren(arena, 3)
		repl[0] = errNode
		repl[1] = expr
		repl[2] = body
	} else {
		errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
		errNode.setHasError(true)
		errNode.setExtra(true)
		repl = phpAllocChildren(arena, 2)
		repl[0] = errNode
		repl[1] = body
	}
	suffix, ok := phpReparsedTopLevelSuffix(source, body.endByte, parser, lang, arena)
	if !ok {
		return nil, 0, false
	}
	combined := phpAllocChildren(arena, len(repl)+len(suffix))
	copy(combined, repl)
	copy(combined[len(repl):], suffix)
	return combined, len(nodes), true
}

func rewritePHPStaticAnonymousFunctionFragments(nodes []*Node, source []byte, lang *Language, arena *nodeArena) ([]*Node, int, bool) {
	if len(nodes) < 3 {
		return nil, 0, false
	}
	errNode := nodes[0]
	openBrace := nodes[1]
	closeBrace := nodes[2]
	if errNode == nil || openBrace == nil || closeBrace == nil {
		return nil, 0, false
	}
	if errNode.Type(lang) != "ERROR" || len(errNode.children) != 1 || errNode.children[0] == nil || errNode.children[0].Type(lang) != "_anonymous_function_header" {
		return nil, 0, false
	}
	header := errNode.children[0]
	if len(header.children) != 3 || header.children[0] == nil || header.children[1] == nil || header.children[2] == nil {
		return nil, 0, false
	}
	if header.children[0].Type(lang) != "static_modifier" || header.children[1].Type(lang) != "function" || header.children[2].Type(lang) != "formal_parameters" {
		return nil, 0, false
	}
	if openBrace.Type(lang) != "{" || closeBrace.Type(lang) != "}" {
		return nil, 0, false
	}
	compoundSym, compoundNamed, ok := symbolMeta(lang, "compound_statement")
	if !ok {
		return nil, 0, false
	}
	bodyChildren := phpAllocChildren(arena, 2)
	bodyChildren[0] = openBrace
	bodyChildren[1] = closeBrace
	body := newParentNodeInArena(arena, compoundSym, compoundNamed, bodyChildren, nil, 0)

	anonSym, anonNamed, ok := symbolMeta(lang, "anonymous_function")
	if !ok {
		return nil, 0, false
	}
	anonChildren := phpAllocChildren(arena, 4)
	anonChildren[0] = header.children[0]
	anonChildren[1] = header.children[1]
	anonChildren[2] = header.children[2]
	anonChildren[3] = body
	anon := newParentNodeInArena(arena, anonSym, anonNamed, anonChildren, phpSyntheticFieldIDs(arena, 4, lang, map[int]string{
		0: "static_modifier",
		2: "parameters",
		3: "body",
	}), 0)

	extraCount := 0
	for 3+extraCount < len(nodes) {
		next := nodes[3+extraCount]
		if next == nil || !next.isExtra() {
			break
		}
		extraCount++
	}

	semiSym, ok := lang.SymbolByName(";")
	if !ok {
		return nil, 0, false
	}
	semiStartByte := closeBrace.endByte
	semiStartPoint := closeBrace.endPoint
	if extraCount > 0 {
		lastExtra := nodes[3+extraCount-1]
		semiStartByte = lastExtra.endByte
		semiStartPoint = lastExtra.endPoint
	}
	semi := newLeafNodeInArena(arena, semiSym, false, semiStartByte, semiStartByte, semiStartPoint, semiStartPoint)
	// Zero-width recovery-synthesized ";" (no source text): tree-sitter-c marks
	// such inserted terminals Missing, not merely error. Match it.
	semi.setMissing(true)
	semi.setHasError(true)

	exprSym, exprNamed, ok := symbolMeta(lang, "expression_statement")
	if !ok {
		return nil, 0, false
	}
	exprChildren := phpAllocChildren(arena, 2+extraCount)
	exprChildren[0] = anon
	for i := 0; i < extraCount; i++ {
		exprChildren[1+i] = nodes[3+i]
	}
	exprChildren[len(exprChildren)-1] = semi
	expr := newParentNodeInArena(arena, exprSym, exprNamed, exprChildren, nil, 0)

	repl := phpAllocChildren(arena, 1)
	repl[0] = expr
	return repl, 3 + extraCount, true
}

func phpSyntheticNamedFunctionName(errNode *Node, lang *Language, arena *nodeArena) (*Node, bool) {
	if errNode == nil || errNode.startByte >= errNode.endByte {
		return nil, false
	}
	nameSym, nameNamed, ok := symbolMeta(lang, "name")
	if !ok {
		return nil, false
	}
	return newLeafNodeInArena(arena, nameSym, nameNamed, errNode.startByte, errNode.endByte, errNode.startPoint, errNode.endPoint), true
}

func phpSyntheticArgumentsFromFormals(formals *Node, lang *Language, arena *nodeArena) (*Node, bool) {
	if formals == nil || formals.Type(lang) != "formal_parameters" || len(formals.children) != 2 {
		return nil, false
	}
	argsSym, argsNamed, ok := symbolMeta(lang, "arguments")
	if !ok {
		return nil, false
	}
	children := phpAllocChildren(arena, 2)
	children[0] = formals.children[0]
	children[1] = formals.children[1]
	return newParentNodeInArena(arena, argsSym, argsNamed, children, nil, 0), true
}

func phpSyntheticCompoundStatementFromError(errNode *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if errNode == nil || errNode.startByte >= errNode.endByte || int(errNode.endByte) > len(source) {
		return nil, false
	}
	body := source[errNode.startByte:errNode.endByte]
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, false
	}
	compoundSym, compoundNamed, ok := symbolMeta(lang, "compound_statement")
	if !ok {
		return nil, false
	}
	openSym, ok := lang.SymbolByName("{")
	if !ok {
		return nil, false
	}
	closeSym, ok := lang.SymbolByName("}")
	if !ok {
		return nil, false
	}
	openEndByte := errNode.startByte + 1
	openEndPoint := advancePointByBytes(errNode.startPoint, source[errNode.startByte:openEndByte])
	closeStartByte := errNode.endByte - 1
	closeStartPoint := advancePointByBytes(errNode.startPoint, source[errNode.startByte:closeStartByte])
	open := newLeafNodeInArena(arena, openSym, false, errNode.startByte, openEndByte, errNode.startPoint, openEndPoint)
	close := newLeafNodeInArena(arena, closeSym, false, closeStartByte, errNode.endByte, closeStartPoint, errNode.endPoint)
	children := phpAllocChildren(arena, 2)
	children[0] = open
	children[1] = close
	return newParentNodeInArena(arena, compoundSym, compoundNamed, children, nil, 0), true
}

func phpSyntheticFieldIDs(arena *nodeArena, childCount int, lang *Language, byIndex map[int]string) []FieldID {
	fieldIDs := make([]FieldID, childCount)
	if arena != nil {
		fieldIDs = arena.allocFieldIDSlice(childCount)
	}
	for idx, name := range byIndex {
		if idx < 0 || idx >= childCount {
			continue
		}
		if fid, ok := lang.FieldByName(name); ok {
			fieldIDs[idx] = fid
		}
	}
	return fieldIDs
}

func phpAllocChildren(arena *nodeArena, n int) []*Node {
	if arena != nil {
		return arena.allocNodeSlice(n)
	}
	return make([]*Node, n)
}

func phpCountsAsPriorTopLevelNode(n *Node, lang *Language) bool {
	return n != nil && !n.isExtra() && (lang == nil || n.Type(lang) != "php_tag")
}

func assignPHPTopLevelFragmentFields(root *Node, lang *Language, arena *nodeArena) {
	if root == nil || lang == nil || lang.Name != "php" || len(root.children) == 0 {
		return
	}
	var fieldIDs []FieldID
	var fieldSources []uint8
	for i := 0; i+6 < len(root.children); i++ {
		if root.children[i] == nil || root.children[i+1] == nil || root.children[i+2] == nil || root.children[i+3] == nil || root.children[i+4] == nil || root.children[i+6] == nil {
			continue
		}
		if root.children[i].Type(lang) != "static_modifier" ||
			root.children[i+1].Type(lang) != "function" ||
			root.children[i+2].Type(lang) != "formal_parameters" ||
			root.children[i+3].Type(lang) != "{" ||
			root.children[i+4].Type(lang) != "ERROR" ||
			root.children[i+6].Type(lang) != "expression_statement" {
			continue
		}
		if fieldIDs == nil {
			if arena != nil {
				fieldIDs = arena.allocFieldIDSlice(len(root.children))
				fieldSources = make([]uint8, len(root.children))
			} else {
				fieldIDs = make([]FieldID, len(root.children))
				fieldSources = make([]uint8, len(root.children))
			}
		}
		if fid, ok := lang.FieldByName("static_modifier"); ok {
			fieldIDs[i] = fid
			fieldSources[i] = fieldSourceDirect
		}
		if fid, ok := lang.FieldByName("parameters"); ok {
			fieldIDs[i+2] = fid
			fieldSources[i+2] = fieldSourceDirect
		}
	}
	if fieldIDs != nil {
		root.setFieldMetadata(fieldIDs, fieldSources)
	}
}

func phpReparsedTopLevelSuffix(source []byte, start uint32, parser *Parser, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if lang == nil || lang.Name != "php" || int(start) >= len(source) {
		return nil, false
	}
	start = phpSkipLeadingLayout(source, start)
	if int(start) >= len(source) {
		return nil, false
	}
	if recovered, ok := phpRecoveredStaticAnonymousSuffix(source, start, parser, lang, arena); ok {
		return recovered, true
	}
	const prefix = "<?php\n"
	wrapped := make([]byte, 0, len(prefix)+len(source)-int(start))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[start:]...)
	tree, err := parseWithSnippetParserInheriting(lang, wrapped, parser)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, false
	}
	defer tree.Release()
	startPoint := advancePointByBytes(Point{}, source[:start])
	prefixPoint := advancePointByBytes(Point{}, []byte(prefix))
	if start < uint32(len(prefix)) || startPoint.Row < prefixPoint.Row {
		return nil, false
	}
	root := tree.RootNode()
	if root == nil || len(root.children) == 0 {
		return nil, false
	}
	offset := &cloneOffset{
		byteDelta: start - uint32(len(prefix)),
		point:     Point{Row: startPoint.Row - prefixPoint.Row, Column: startPoint.Column},
		baseRow:   root.startPoint.Row,
	}
	out := make([]*Node, 0, len(root.children))
	for _, child := range root.children {
		if child == nil || child.Type(lang) == "php_tag" {
			continue
		}
		out = append(out, child)
	}
	if len(out) == 0 {
		return nil, false
	}
	if parser != nil && phpCanBorrowOffsetSuffixNodes(out) {
		for _, child := range out {
			phpApplyCloneOffsetInPlace(child, offset)
		}
		parser.borrowCompatibilityTreeArenas(tree)
		return out, true
	}
	for i, child := range out {
		out[i] = cloneTreeNodesIntoArenaWithOffset(child, arena, offset)
	}
	return out, len(out) > 0
}

func phpRecoveredStaticAnonymousSuffix(source []byte, start uint32, parser *Parser, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if lang == nil || lang.Name != "php" || int(start) >= len(source) {
		return nil, false
	}
	cursor := start
	out := make([]*Node, 0, 8)
	for {
		comment, next, ok := phpLineCommentNodeAt(source, cursor, arena, lang)
		if !ok {
			break
		}
		out = append(out, comment)
		cursor = phpSkipLeadingLayout(source, next)
	}
	staticStart := cursor
	staticEnd, ok := phpConsumeWord(source, cursor, "static")
	if !ok {
		return nil, false
	}
	cursor = phpSkipInlineLayout(source, staticEnd)
	functionStart := cursor
	functionEnd, ok := phpConsumeWord(source, cursor, "function")
	if !ok {
		return nil, false
	}
	cursor = phpSkipInlineLayout(source, functionEnd)
	openParen := cursor
	if int(openParen) >= len(source) || source[openParen] != '(' {
		return nil, false
	}
	cursor = phpSkipInlineLayout(source, openParen+1)
	closeParen := cursor
	if int(closeParen) >= len(source) || source[closeParen] != ')' {
		return nil, false
	}
	cursor = phpSkipInlineLayout(source, closeParen+1)
	openBrace := cursor
	if int(openBrace) >= len(source) || source[openBrace] != '{' {
		return nil, false
	}
	cursor = phpSkipInlineLayout(source, openBrace+1)
	closeBrace := cursor
	if int(closeBrace) >= len(source) || source[closeBrace] != '}' {
		return nil, false
	}
	afterClose := closeBrace + 1

	staticModifier, ok := phpBuildStaticModifierNode(source, staticStart, staticEnd, arena, lang)
	if !ok {
		return nil, false
	}
	functionNode, ok := phpBuildLeafByName(source, functionStart, functionEnd, "function", arena, lang)
	if !ok {
		return nil, false
	}
	formals, ok := phpBuildFormalParametersNode(source, openParen, closeParen+1, arena, lang)
	if !ok {
		return nil, false
	}
	openBraceNode, ok := phpBuildAnonymousLeafByName(source, openBrace, openBrace+1, "{", arena, lang)
	if !ok {
		return nil, false
	}
	closeErr, ok := phpBuildExtraErrorLeafWrapper(source, closeBrace, closeBrace+1, "}", arena, lang)
	if !ok {
		return nil, false
	}
	out = append(out, staticModifier, functionNode, formals, openBraceNode, closeErr)

	cursor = phpSkipLeadingLayout(source, afterClose)
	if comment, next, ok := phpLineCommentNodeAt(source, cursor, arena, lang); ok {
		out = append(out, comment)
		cursor = phpSkipLeadingLayout(source, next)
	}
	if int(cursor) >= len(source) {
		return nil, false
	}
	suffix, ok := phpReparsedTopLevelSuffix(source, cursor, parser, lang, arena)
	if !ok {
		return nil, false
	}
	combined := phpAllocChildren(arena, len(out)+len(suffix))
	copy(combined, out)
	copy(combined[len(out):], suffix)
	return combined, true
}

func phpSkipLeadingLayout(source []byte, start uint32) uint32 {
	for int(start) < len(source) {
		switch source[start] {
		case ' ', '\t', '\n', '\r':
			start++
		default:
			return start
		}
	}
	return start
}

func phpSkipInlineLayout(source []byte, start uint32) uint32 {
	for int(start) < len(source) {
		switch source[start] {
		case ' ', '\t':
			start++
		default:
			return start
		}
	}
	return start
}

func phpConsumeWord(source []byte, start uint32, word string) (uint32, bool) {
	end := start + uint32(len(word))
	if int(end) > len(source) {
		return start, false
	}
	for i := range word {
		if source[int(start)+i] != word[i] {
			return start, false
		}
	}
	if int(end) < len(source) && phpIsNameByte(source[end]) {
		return start, false
	}
	return end, true
}

func phpIsNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

func phpLineCommentNodeAt(source []byte, start uint32, arena *nodeArena, lang *Language) (*Node, uint32, bool) {
	if int(start)+1 >= len(source) || source[start] != '/' || source[start+1] != '/' {
		return nil, start, false
	}
	end := start + 2
	for int(end) < len(source) && source[end] != '\n' && source[end] != '\r' {
		end++
	}
	node, ok := phpBuildLeafByName(source, start, end, "comment", arena, lang)
	if !ok {
		return nil, start, false
	}
	node.setExtra(true)
	return node, end, true
}

func phpBuildStaticModifierNode(source []byte, start, end uint32, arena *nodeArena, lang *Language) (*Node, bool) {
	parentSym, parentNamed, ok := symbolMeta(lang, "static_modifier")
	if !ok {
		return nil, false
	}
	child, ok := phpBuildAnonymousLeafByName(source, start, end, "static", arena, lang)
	if !ok {
		return nil, false
	}
	children := phpAllocChildren(arena, 1)
	children[0] = child
	return newParentNodeInArena(arena, parentSym, parentNamed, children, nil, 0), true
}

func phpBuildFormalParametersNode(source []byte, start, end uint32, arena *nodeArena, lang *Language) (*Node, bool) {
	if end != start+2 || int(end) > len(source) || source[start] != '(' || source[start+1] != ')' {
		return nil, false
	}
	formalsSym, formalsNamed, ok := symbolMeta(lang, "formal_parameters")
	if !ok {
		return nil, false
	}
	open, ok := phpBuildAnonymousLeafByName(source, start, start+1, "(", arena, lang)
	if !ok {
		return nil, false
	}
	close, ok := phpBuildAnonymousLeafByName(source, start+1, end, ")", arena, lang)
	if !ok {
		return nil, false
	}
	children := phpAllocChildren(arena, 2)
	children[0] = open
	children[1] = close
	return newParentNodeInArena(arena, formalsSym, formalsNamed, children, nil, 0), true
}

func phpBuildExtraErrorLeafWrapper(source []byte, start, end uint32, name string, arena *nodeArena, lang *Language) (*Node, bool) {
	child, ok := phpBuildAnonymousLeafByName(source, start, end, name, arena, lang)
	if !ok {
		return nil, false
	}
	children := phpAllocChildren(arena, 1)
	children[0] = child
	errNode := newParentNodeInArena(arena, errorSymbol, true, children, nil, 0)
	errNode.setHasError(true)
	errNode.setExtra(true)
	return errNode, true
}

func phpBuildLeafByName(source []byte, start, end uint32, name string, arena *nodeArena, lang *Language) (*Node, bool) {
	sym, named, ok := symbolMeta(lang, name)
	if !ok {
		return nil, false
	}
	return phpBuildLeaf(source, start, end, sym, named, arena), true
}

func phpBuildAnonymousLeafByName(source []byte, start, end uint32, name string, arena *nodeArena, lang *Language) (*Node, bool) {
	sym, ok := phpAnonymousSymbol(lang, name)
	if !ok {
		return nil, false
	}
	return phpBuildLeaf(source, start, end, sym, symbolIsNamed(lang, sym), arena), true
}

func phpBuildLeaf(source []byte, start, end uint32, sym Symbol, named bool, arena *nodeArena) *Node {
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	return newLeafNodeInArena(arena, sym, named, start, end, startPoint, endPoint)
}

func phpCanBorrowOffsetSuffixNodes(nodes []*Node) bool {
	for _, root := range nodes {
		if !phpCanBorrowOffsetSuffixNode(root) {
			return false
		}
	}
	return true
}

func phpCanBorrowOffsetSuffixNode(root *Node) bool {
	if root == nil {
		return true
	}
	stack := []*Node{root}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if node == nil {
			continue
		}
		if node.ownerArena != nil {
			if _, ok := node.ownerArena.finalChildRange(node); ok {
				return false
			}
		}
		stack = append(stack, node.children...)
	}
	return true
}

func phpApplyCloneOffsetInPlace(root *Node, offset *cloneOffset) {
	if root == nil || offset == nil {
		return
	}
	stack := []*Node{root}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if node == nil {
			continue
		}
		node.startByte = addUint32Delta(node.startByte, int64(offset.byteDelta))
		node.endByte = addUint32Delta(node.endByte, int64(offset.byteDelta))
		node.startPoint = offset.offsetPoint(node.startPoint)
		node.endPoint = offset.offsetPoint(node.endPoint)
		stack = append(stack, node.children...)
	}
}
