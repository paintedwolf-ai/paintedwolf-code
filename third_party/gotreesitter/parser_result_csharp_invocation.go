package gotreesitter

func normalizeCSharpInvocationStatements(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" || len(source) == 0 {
		return
	}
	exprStmtSym, ok := lang.SymbolByName("expression_statement")
	if !ok {
		return
	}
	invocationSym, ok := lang.SymbolByName("invocation_expression")
	if !ok {
		return
	}
	implicitObjectCreationSym, ok := lang.SymbolByName("implicit_object_creation_expression")
	if !ok {
		return
	}
	newSym, ok := lang.SymbolByName("new")
	if !ok {
		return
	}
	memberAccessSym, ok := lang.SymbolByName("member_access_expression")
	if !ok {
		return
	}
	argumentListSym, ok := lang.SymbolByName("argument_list")
	if !ok {
		return
	}
	argumentSym, ok := lang.SymbolByName("argument")
	if !ok {
		return
	}
	functionFieldID, hasFunctionField := lang.FieldByName("function")
	argumentsFieldID, hasArgumentsField := lang.FieldByName("arguments")
	expressionFieldID, hasExpressionField := lang.FieldByName("expression")
	nameFieldID, hasNameField := lang.FieldByName("name")
	if !hasFunctionField || !hasArgumentsField || !hasExpressionField || !hasNameField {
		return
	}
	exprStmtNamed := symbolIsNamed(lang, exprStmtSym)
	invocationNamed := symbolIsNamed(lang, invocationSym)
	implicitObjectCreationNamed := symbolIsNamed(lang, implicitObjectCreationSym)
	newNamed := symbolIsNamed(lang, newSym)
	memberAccessNamed := symbolIsNamed(lang, memberAccessSym)
	argumentListNamed := symbolIsNamed(lang, argumentListSym)
	argumentNamed := symbolIsNamed(lang, argumentSym)

	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "member_access_expression" && len(n.children) > 0 {
			if first := n.children[0]; first != nil && first.Type(lang) == "qualified_name" {
				n.children[0] = csharpRewriteQualifiedNameAsMemberAccess(first, lang, memberAccessSym, memberAccessNamed, expressionFieldID, nameFieldID)
				n.children[0].parent = n
				n.children[0].childIndex = 0
				n.setHasError(false)
			}
		}
		if n.Type(lang) == "argument_list" {
			csharpPopulateMissingInvocationArguments(n, source, lang)
		}
		if n.Type(lang) == "invocation_expression" {
			csharpRewriteImplicitObjectCreationInvocation(n, source, lang, implicitObjectCreationSym, implicitObjectCreationNamed, newSym, newNamed)
		}
		if n.Type(lang) == "variable_declarator" {
			csharpRewriteGenericInvocationInitializer(n, source, lang)
		}
		if n.Type(lang) == "local_declaration_statement" && len(n.children) == 2 {
			decl := n.children[0]
			semi := n.children[1]
			if decl != nil && semi != nil && semi.Type(lang) == ";" &&
				decl.Type(lang) == "variable_declaration" && len(decl.children) == 2 {
				target := decl.children[0]
				varDecl := decl.children[1]
				if target != nil && varDecl != nil &&
					(target.Type(lang) == "identifier" || target.Type(lang) == "qualified_name") &&
					varDecl.Type(lang) == "variable_declarator" &&
					len(varDecl.children) == 1 &&
					varDecl.children[0] != nil &&
					varDecl.children[0].Type(lang) == "tuple_pattern" {
					function := target
					if target.Type(lang) == "qualified_name" {
						function = csharpRewriteQualifiedNameAsMemberAccess(target, lang, memberAccessSym, memberAccessNamed, expressionFieldID, nameFieldID)
					}
					if arguments, ok := csharpBuildArgumentListFromTuplePattern(varDecl.children[0], lang, argumentListSym, argumentListNamed, argumentSym, argumentNamed); ok {
						invocationFields := cloneFieldIDSliceInArena(n.ownerArena, []FieldID{functionFieldID, argumentsFieldID})
						invocation := newParentNodeInArena(n.ownerArena, invocationSym, invocationNamed, []*Node{function, arguments}, invocationFields, 0)
						n.symbol = exprStmtSym
						n.setNamed(exprStmtNamed)
						replaceNodeChildrenUnfielded(n, cloneNodeSliceIfArena(n.ownerArena, []*Node{invocation, semi}))
						n.productionID = 0
						n.setHasError(false)
					}
				}
			}
		}
	})
}

func csharpRewriteGenericInvocationInitializer(declarator *Node, source []byte, lang *Language) bool {
	if declarator == nil || declarator.ownerArena == nil || resultChildCount(declarator) < 3 {
		return false
	}
	valueIdx := -1
	for i := resultChildCount(declarator) - 1; i >= 0; i-- {
		child := resultChildAt(declarator, i)
		if child != nil && child.IsNamed() && child.Type(lang) != "identifier" {
			valueIdx = i
			break
		}
	}
	if valueIdx < 0 {
		return false
	}
	value := resultChildAt(declarator, valueIdx)
	if value == nil || value.Type(lang) != "binary_expression" || !csharpLooksLikeGenericInvocationSource(source, value.startByte, value.endByte) {
		return false
	}
	invocation, ok := csharpBuildGenericInvocationExpressionNode(declarator.ownerArena, source, lang, value.startByte, value.endByte)
	if !ok || invocation == nil || invocation.Type(lang) != "invocation_expression" {
		return false
	}
	children := resultChildSliceForMutation(declarator)
	if valueIdx >= len(children) {
		return false
	}
	children[valueIdx] = invocation
	declarator.children = cloneNodeSliceIfArena(declarator.ownerArena, children)
	declarator.productionID = 0
	declarator.setHasError(false)
	populateParentNode(declarator, declarator.children)
	return true
}

func csharpBuildGenericInvocationExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	openParen, ok := csharpFindInvocationOpenParen(source, start, end)
	if !ok {
		return nil, false
	}
	functionEnd := csharpTrimRightSpaceBytes(source, openParen)
	function, ok := csharpBuildGenericNameNode(arena, source, lang, start, functionEnd)
	if !ok {
		return nil, false
	}
	args, ok := csharpBuildArgumentListNode(arena, source, lang, openParen, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "invocation_expression")
	if !ok {
		return nil, false
	}
	functionID, _ := lang.FieldByName("function")
	argumentsID, _ := lang.FieldByName("arguments")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{functionID, argumentsID})
	named := symbolIsNamed(lang, sym)
	node := newParentNodeInArena(arena, sym, named, []*Node{function, args}, fields, 0)
	node.setHasError(false)
	return node, true
}

func csharpLooksLikeGenericInvocationSource(source []byte, start, end uint32) bool {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || source[end-1] != ')' {
		return false
	}
	openParen, ok := csharpFindInvocationOpenParen(source, start, end)
	if !ok || openParen <= start {
		return false
	}
	beforeParen := csharpTrimRightSpaceBytes(source, openParen)
	if beforeParen <= start || source[beforeParen-1] != '>' {
		return false
	}
	ltPos, ok := csharpFindGenericTypeArgumentOpen(source, start, beforeParen)
	if !ok || ltPos <= start {
		return false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, start)
	return ok && nameStart == start && nameEnd == ltPos
}

func csharpRewriteImplicitObjectCreationInvocation(n *Node, source []byte, lang *Language, implicitObjectCreationSym Symbol, implicitObjectCreationNamed bool, newSym Symbol, newNamed bool) bool {
	if n == nil || lang == nil || resultChildCount(n) != 2 || len(source) == 0 {
		return false
	}
	newNode := resultChildAt(n, 0)
	args := resultChildAt(n, 1)
	if newNode == nil || args == nil || newNode.Type(lang) != "identifier" || args.Type(lang) != "argument_list" {
		return false
	}
	if newNode.startByte >= newNode.endByte || int(newNode.endByte) > len(source) || string(source[newNode.startByte:newNode.endByte]) != "new" {
		return false
	}
	if args.startByte != newNode.endByte {
		return false
	}
	retagResultRoot(newNode, newSym, newNamed)
	n.symbol = implicitObjectCreationSym
	n.setNamed(implicitObjectCreationNamed)
	n.clearFieldMetadata()
	n.productionID = 0
	n.setHasError(false)
	return true
}

func csharpPopulateMissingInvocationArguments(n *Node, source []byte, lang *Language) bool {
	if n == nil || lang == nil || n.ownerArena == nil || n.Type(lang) != "argument_list" || len(source) == 0 {
		return false
	}
	if n.startByte >= n.endByte || int(n.endByte) > len(source) {
		return false
	}
	if n.startByte+1 >= n.endByte || source[n.startByte] != '(' || source[n.endByte-1] != ')' {
		return false
	}
	innerStart, innerEnd := csharpTrimSpaceBounds(source, n.startByte+1, n.endByte-1)
	if innerStart >= innerEnd {
		return false
	}
	for _, child := range n.children {
		if child != nil && child.IsNamed() {
			return false
		}
	}
	rebuilt, ok := csharpBuildArgumentListNode(n.ownerArena, source, lang, n.startByte, n.endByte)
	if !ok || rebuilt == nil {
		return false
	}
	csharpReplaceNodeContents(n, rebuilt)
	return true
}

func csharpRecoverTopLevelInvocationStatementFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || source[end-1] != ';' {
		return nil, false
	}
	exprEnd := csharpTrimRightSpaceBytes(source, end-1)
	if exprEnd <= start {
		return nil, false
	}
	invocation, ok := csharpBuildInvocationExpressionNode(arena, source, lang, start, exprEnd)
	if !ok || invocation == nil {
		return nil, false
	}
	if invocation.Type(lang) != "invocation_expression" || len(invocation.children) == 0 || invocation.children[0] == nil {
		return nil, false
	}
	semiTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end)
	if !ok {
		return nil, false
	}
	exprStmtSym, ok := symbolByName(lang, "expression_statement")
	if !ok {
		return nil, false
	}
	globalSym, ok := symbolByName(lang, "global_statement")
	if !ok {
		return nil, false
	}
	exprStmtNamed := symbolIsNamed(lang, exprStmtSym)
	globalNamed := symbolIsNamed(lang, globalSym)
	expressionFieldID, _ := lang.FieldByName("expression")
	exprChildren := []*Node{invocation, semiTok}
	if arena != nil {
		buf := arena.allocNodeSlice(len(exprChildren))
		copy(buf, exprChildren)
		exprChildren = buf
	}
	exprFieldIDs := cloneFieldIDSliceInArena(arena, []FieldID{expressionFieldID, 0})
	exprStmt := newParentNodeInArena(arena, exprStmtSym, exprStmtNamed, exprChildren, exprFieldIDs, 0)
	exprStmt.setHasError(false)
	globalChildren := []*Node{exprStmt}
	if arena != nil {
		buf := arena.allocNodeSlice(len(globalChildren))
		copy(buf, globalChildren)
		globalChildren = buf
	}
	global := newParentNodeInArena(arena, globalSym, globalNamed, globalChildren, nil, 0)
	global.setHasError(false)
	return global, true
}

func csharpRewriteQualifiedNameAsMemberAccess(node *Node, lang *Language, memberAccessSym Symbol, memberAccessNamed bool, expressionFieldID, nameFieldID FieldID) *Node {
	if node == nil || lang == nil || node.Type(lang) != "qualified_name" {
		return node
	}
	childCount := len(node.children)
	fieldIDs := make([]FieldID, childCount)
	if node.ownerArena != nil && childCount > 0 {
		buf := node.ownerArena.allocFieldIDSlice(childCount)
		copy(buf, fieldIDs)
		fieldIDs = buf
	}
	if childCount > 0 {
		fieldIDs[0] = expressionFieldID
	}
	if childCount > 2 {
		fieldIDs[2] = nameFieldID
	}
	node.symbol = memberAccessSym
	node.setNamed(memberAccessNamed)
	node.setFieldMetadata(fieldIDs, defaultFieldSourcesInArena(node.ownerArena, fieldIDs))
	node.productionID = 0
	node.setHasError(false)
	populateParentNode(node, node.children)
	return node
}

func csharpBuildArgumentListFromTuplePattern(tuple *Node, lang *Language, argumentListSym Symbol, argumentListNamed bool, argumentSym Symbol, argumentNamed bool) (*Node, bool) {
	if tuple == nil || lang == nil || tuple.Type(lang) != "tuple_pattern" || len(tuple.children) < 3 {
		return nil, false
	}
	children := make([]*Node, 0, len(tuple.children))
	children = append(children, tuple.children[0])
	for i := 1; i < len(tuple.children)-1; i++ {
		child := tuple.children[i]
		if child == nil {
			continue
		}
		if child.IsNamed() {
			argChildren := []*Node{child}
			argChildren = cloneNodeSliceIfArena(tuple.ownerArena, argChildren)
			arg := newParentNodeInArena(tuple.ownerArena, argumentSym, argumentNamed, argChildren, nil, 0)
			arg.setHasError(false)
			children = append(children, arg)
			continue
		}
		children = append(children, child)
	}
	children = append(children, tuple.children[len(tuple.children)-1])
	children = cloneNodeSliceIfArena(tuple.ownerArena, children)
	args := newParentNodeInArena(tuple.ownerArena, argumentListSym, argumentListNamed, children, nil, 0)
	args.setHasError(false)
	return args, true
}

func normalizeCSharpSwitchTupleCasePatterns(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" {
		return
	}
	patternSym, ok := lang.SymbolByName("constant_pattern")
	if !ok {
		return
	}
	tupleExprSym, ok := lang.SymbolByName("tuple_expression")
	if !ok {
		return
	}
	argumentSym, ok := lang.SymbolByName("argument")
	if !ok {
		return
	}
	named := symbolIsNamed(lang, patternSym)
	tupleNamed := symbolIsNamed(lang, tupleExprSym)
	argumentNamed := symbolIsNamed(lang, argumentSym)
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "switch_section" && len(n.children) > 1 {
			pat := n.children[1]
			if n.children[0] != nil && n.children[0].Type(lang) == "case" &&
				pat != nil && csharpShouldWrapSwitchCaseConstantPattern(pat, lang) {
				repl := newParentNodeInArena(n.ownerArena, patternSym, named, []*Node{pat}, nil, 0)
				repl.parent = n
				repl.childIndex = 1
				n.children[1] = repl
				pat = repl
			}
			if n.children[0] != nil && n.children[0].Type(lang) == "case" &&
				pat != nil && (pat.Type(lang) == "tuple_expression" || pat.Type(lang) == "recursive_pattern") {
				tuple := pat
				if pat.Type(lang) != "tuple_expression" {
					tupleChildren, ok := csharpTupleExpressionChildrenFromRecursivePattern(pat, lang, argumentSym, argumentNamed)
					if !ok {
						tupleChildren = append([]*Node(nil), pat.children...)
						if n.ownerArena != nil && len(tupleChildren) > 0 {
							buf := n.ownerArena.allocNodeSlice(len(tupleChildren))
							copy(buf, tupleChildren)
							tupleChildren = buf
						}
					}
					tuple = newParentNodeInArena(n.ownerArena, tupleExprSym, tupleNamed, tupleChildren, nil, 0)
				}
				repl := newParentNodeInArena(n.ownerArena, patternSym, named, []*Node{tuple}, nil, 0)
				repl.parent = n
				repl.childIndex = 1
				n.children[1] = repl
				pat = repl
			}
		}
	})
}

func csharpTupleExpressionChildrenFromRecursivePattern(pat *Node, lang *Language, argumentSym Symbol, argumentNamed bool) ([]*Node, bool) {
	if pat == nil || lang == nil || len(pat.children) != 1 || pat.children[0] == nil || pat.children[0].Type(lang) != "positional_pattern_clause" {
		return nil, false
	}
	clause := pat.children[0]
	if len(clause.children) < 3 {
		return nil, false
	}
	children := make([]*Node, 0, len(clause.children))
	for _, child := range clause.children {
		if child == nil {
			continue
		}
		if !child.IsNamed() {
			children = append(children, child)
			continue
		}
		if child.Type(lang) != "subpattern" || len(child.children) != 1 || child.children[0] == nil {
			return nil, false
		}
		value := child.children[0]
		if value.Type(lang) == "constant_pattern" && len(value.children) == 1 && value.children[0] != nil {
			value = value.children[0]
		}
		argChildren := cloneNodeSliceIfArena(pat.ownerArena, []*Node{value})
		arg := newParentNodeInArena(pat.ownerArena, argumentSym, argumentNamed, argChildren, nil, 0)
		arg.setHasError(false)
		children = append(children, arg)
	}
	if len(children) == 0 {
		return nil, false
	}
	children = cloneNodeSliceIfArena(pat.ownerArena, children)
	return children, true
}

func csharpShouldWrapSwitchCaseConstantPattern(n *Node, lang *Language) bool {
	if n == nil || lang == nil {
		return false
	}
	switch n.Type(lang) {
	case "integer_literal", "real_literal", "string_literal", "character_literal", "null_literal", "boolean_literal", "identifier", "member_access_expression":
		return true
	default:
		return false
	}
}
