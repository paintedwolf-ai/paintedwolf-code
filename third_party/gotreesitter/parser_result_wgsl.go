package gotreesitter

func normalizeWGSLCompatibility(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "wgsl" {
		return
	}
	walkResultTree(root, func(n *Node) {
		normalizeWGSLEmptyReturnSemicolonRecovery(n, lang)
		normalizeWGSLArgumentListErrorWrapper(n, lang)
		normalizeWGSLTrailingArgumentMissingIdentifier(n, lang)
		normalizeWGSLAtomicArrayRecovery(n, lang)
		normalizeWGSLRecoveredCallLHSWrapper(n, lang)
		normalizeWGSLConstAssignmentRecovery(n, lang)
		normalizeWGSLRecoveredCallAssignment(n, lang)
		normalizeWGSLRecoveredU32CallArgument(n, lang)
	})
	refreshWGSLHasError(root)
}

func normalizeWGSLEmptyReturnSemicolonRecovery(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "compound_statement" || resultChildCount(n) < 2 {
		return
	}
	errSym, errNamed := errorSymbol, true
	children := resultChildSliceForMutation(n)
	if len(children) < 2 {
		return
	}
	out := make([]*Node, 0, len(children))
	changed := false
	for i := 0; i < len(children); i++ {
		cur := children[i]
		if i+1 < len(children) && wgslIsEmptyReturnStatement(cur, lang) {
			next := children[i+1]
			if next != nil && next.Type(lang) == ";" && next.StartByte() == cur.StartByte() && next.EndByte() == cur.StartByte()+1 {
				err := newParentNodeInArena(n.ownerArena, errSym, errNamed, []*Node{next}, nil, 0)
				err.setHasError(true)
				out = append(out, err)
				i++
				changed = true
				continue
			}
		}
		out = append(out, cur)
	}
	if changed {
		replaceNodeChildrenUnfielded(n, out)
		n.setHasError(true)
	}
}

func wgslIsEmptyReturnStatement(n *Node, lang *Language) bool {
	if n == nil || n.Type(lang) != "return_statement" || n.StartByte() != n.EndByte() {
		return false
	}
	switch resultChildCount(n) {
	case 0:
		return true
	case 1:
		child := resultChildAt(n, 0)
		return child != nil && child.Type(lang) == "return" && child.IsMissing() &&
			child.StartByte() == n.StartByte() && child.EndByte() == n.EndByte()
	default:
		return false
	}
}

func normalizeWGSLArgumentListErrorWrapper(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "argument_list_expression" || resultChildCount(n) != 3 {
		return
	}
	open := resultChildAt(n, 0)
	err := resultChildAt(n, 1)
	close := resultChildAt(n, 2)
	if open == nil || open.Type(lang) != "(" || err == nil || !err.IsError() || close == nil || close.Type(lang) != ")" {
		return
	}
	if err.StartByte() != open.EndByte() || err.EndByte() != close.StartByte() || resultChildCount(err) == 0 {
		return
	}
	errChildren := resultChildSliceForMutation(err)
	if !wgslLooksLikeArgumentSequence(errChildren, lang) {
		return
	}
	out := make([]*Node, 0, len(errChildren)+2)
	out = append(out, open)
	out = append(out, errChildren...)
	out = append(out, close)
	replaceNodeChildrenUnfielded(n, out)
}

func wgslLooksLikeArgumentSequence(children []*Node, lang *Language) bool {
	wantExpr := true
	seenExpr := false
	for _, child := range children {
		if child == nil {
			return false
		}
		if wantExpr {
			if child.Type(lang) == "," || child.Type(lang) == ")" || child.IsError() || child.HasError() {
				return false
			}
			seenExpr = true
			wantExpr = false
			continue
		}
		if child.Type(lang) != "," {
			return false
		}
		wantExpr = true
	}
	return seenExpr && !wantExpr
}

func normalizeWGSLTrailingArgumentMissingIdentifier(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "argument_list_expression" || resultChildCount(n) < 4 {
		return
	}
	children := resultChildSliceForMutation(n)
	if len(children) < 4 {
		return
	}
	close := children[len(children)-1]
	missing := children[len(children)-2]
	comma := children[len(children)-3]
	if close == nil || close.Type(lang) != ")" ||
		missing == nil || missing.Type(lang) != "identifier" || missing.StartByte() != missing.EndByte() ||
		comma == nil || comma.Type(lang) != "," || comma.EndByte() != missing.StartByte() {
		return
	}
	out := make([]*Node, 0, len(children)-1)
	out = append(out, children[:len(children)-2]...)
	out = append(out, close)
	replaceNodeChildrenUnfielded(n, out)
}

func normalizeWGSLAtomicArrayRecovery(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "type_declaration" || resultChildCount(n) != 5 {
		return
	}
	children := resultChildSliceForMutation(n)
	if len(children) != 5 ||
		children[0] == nil || children[0].Type(lang) != "array" ||
		children[1] == nil || !children[1].IsError() ||
		children[2] == nil || children[2].Type(lang) != "<" ||
		children[3] == nil || children[3].Type(lang) != "type_declaration" ||
		children[4] == nil || children[4].Type(lang) != ">" {
		return
	}
	errChildren := resultChildSliceForMutation(children[1])
	if len(errChildren) != 2 ||
		errChildren[0] == nil || errChildren[0].Type(lang) != "<" ||
		errChildren[1] == nil || children[1].EndByte() != children[2].StartByte() {
		return
	}
	atomicType := errChildren[1]
	if atomicType.Type(lang) != "type_declaration" || resultChildCount(atomicType) != 1 {
		return
	}
	atomicIdent := resultChildAt(atomicType, 0)
	if atomicIdent == nil || atomicIdent.Type(lang) != "identifier" || atomicIdent.StartByte() != errChildren[0].EndByte() {
		return
	}
	open := errChildren[0]
	nestedOpen := children[2]
	open.setHasError(false)
	atomicType.setHasError(false)
	atomicIdent.setHasError(false)
	nestedOpen.setHasError(false)
	err := newParentNodeInArena(n.ownerArena, errorSymbol, true, []*Node{atomicType, nestedOpen}, nil, 0)
	err.setHasError(true)
	err.setExtra(true)
	replaceNodeChildrenUnfielded(n, []*Node{children[0], open, err, children[3], children[4]})
}

func normalizeWGSLRecoveredCallLHSWrapper(n *Node, lang *Language) {
	if n == nil || !n.IsError() || resultChildCount(n) == 0 {
		return
	}
	children := resultChildSliceForMutation(n)
	changed := false
	for i, child := range children {
		if child == nil || child.Type(lang) != "lhs_expression" || resultChildCount(child) != 1 {
			continue
		}
		inner := resultChildAt(child, 0)
		if inner == nil || inner.Type(lang) != "identifier" || inner.StartByte() != child.StartByte() || inner.EndByte() != child.EndByte() {
			continue
		}
		children[i] = inner
		changed = true
	}
	if changed {
		replaceNodeChildrenUnfielded(n, children)
	}
}

func normalizeWGSLConstAssignmentRecovery(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "compound_statement" || resultChildCount(n) < 2 {
		return
	}
	lhsSym, lhsNamed, ok := symbolMeta(lang, "lhs_expression")
	if !ok {
		return
	}
	children := resultChildSliceForMutation(n)
	out := make([]*Node, 0, len(children))
	changed := false
	for i := 0; i < len(children); i++ {
		cur := children[i]
		if i+1 < len(children) && wgslIsConstKeywordError(cur, lang) && wgslLooksLikeConstAssignmentTail(children[i+1], lang) {
			assign := children[i+1]
			assignChildren := resultChildSliceForMutation(assign)
			constIdent := resultChildAt(cur, 0)
			nameLHS := assignChildren[0]
			nameIdent := resultChildAt(nameLHS, 0)
			constLHS := newParentNodeInArena(n.ownerArena, lhsSym, lhsNamed, []*Node{constIdent}, nil, 0)
			nameErr := newParentNodeInArena(n.ownerArena, errorSymbol, true, []*Node{nameIdent}, nil, 0)
			nameErr.setExtra(true)
			nameErr.setHasError(true)
			rebuiltChildren := make([]*Node, 0, len(assignChildren)+1)
			rebuiltChildren = append(rebuiltChildren, constLHS, nameErr)
			rebuiltChildren = append(rebuiltChildren, assignChildren[1:]...)
			replaceNodeChildrenUnfielded(assign, rebuiltChildren)
			assign.startByte = cur.StartByte()
			assign.startPoint = cur.StartPoint()
			assign.setHasError(true)
			out = append(out, assign)
			i++
			changed = true
			continue
		}
		out = append(out, cur)
	}
	if changed {
		replaceNodeChildrenUnfielded(n, out)
		n.setHasError(true)
	}
}

func wgslIsConstKeywordError(n *Node, lang *Language) bool {
	if n == nil || !n.IsError() || resultChildCount(n) != 1 {
		return false
	}
	child := resultChildAt(n, 0)
	return child != nil && child.Type(lang) == "identifier" && child.StartByte() == n.StartByte() &&
		child.EndByte() == n.EndByte()
}

func wgslLooksLikeConstAssignmentTail(n *Node, lang *Language) bool {
	if n == nil || n.Type(lang) != "assignment_statement" || resultChildCount(n) < 3 {
		return false
	}
	lhs := resultChildAt(n, 0)
	if lhs == nil || lhs.Type(lang) != "lhs_expression" || resultChildCount(lhs) != 1 {
		return false
	}
	name := resultChildAt(lhs, 0)
	eq := resultChildAt(n, 1)
	return name != nil && name.Type(lang) == "identifier" &&
		eq != nil && eq.Type(lang) == "=" &&
		name.EndByte() == name.StartByte()+1 &&
		name.EndByte() < eq.StartByte()
}

type wgslRecoveredCallAssignmentSymbols struct {
	assignmentStatement              Symbol
	assignmentStatementNamed         bool
	lhsExpression                    Symbol
	lhsExpressionNamed               bool
	compoundAssignmentOperator       Symbol
	compoundAssignmentOperatorNamed  bool
	plusEq                           Symbol
	plusEqNamed                      bool
	parenthesizedExpression          Symbol
	parenthesizedExpressionNamed     bool
	binaryExpression                 Symbol
	binaryExpressionNamed            bool
	compositeValueDecomposition      Symbol
	compositeValueDecompositionNamed bool
}

func normalizeWGSLRecoveredCallAssignment(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "compound_statement" || resultChildCount(n) == 0 {
		return
	}
	syms, ok := wgslRecoveredCallAssignmentSymbolSet(lang)
	if !ok {
		return
	}
	children := resultChildSliceForMutation(n)
	out := make([]*Node, 0, len(children)+1)
	changed := false
	for _, child := range children {
		if assignment, semi, ok := wgslBuildRecoveredCallAssignment(child, lang, syms); ok {
			out = append(out, assignment, semi)
			changed = true
			continue
		}
		out = append(out, child)
	}
	if changed {
		replaceNodeChildrenUnfielded(n, out)
		n.setHasError(true)
	}
}

func wgslRecoveredCallAssignmentSymbolSet(lang *Language) (wgslRecoveredCallAssignmentSymbols, bool) {
	assignSym, assignNamed, ok := symbolMeta(lang, "assignment_statement")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	lhsSym, lhsNamed, ok := symbolMeta(lang, "lhs_expression")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	compoundSym, compoundNamed, ok := symbolMeta(lang, "compound_assignment_operator")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	plusEqSym, plusEqNamed, ok := symbolMeta(lang, "+=")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	parenSym, parenNamed, ok := symbolMeta(lang, "parenthesized_expression")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	binarySym, binaryNamed, ok := symbolMeta(lang, "binary_expression")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	compositeSym, compositeNamed, ok := symbolMeta(lang, "composite_value_decomposition_expression")
	if !ok {
		return wgslRecoveredCallAssignmentSymbols{}, false
	}
	return wgslRecoveredCallAssignmentSymbols{
		assignmentStatement:              assignSym,
		assignmentStatementNamed:         assignNamed,
		lhsExpression:                    lhsSym,
		lhsExpressionNamed:               lhsNamed,
		compoundAssignmentOperator:       compoundSym,
		compoundAssignmentOperatorNamed:  compoundNamed,
		plusEq:                           plusEqSym,
		plusEqNamed:                      plusEqNamed,
		parenthesizedExpression:          parenSym,
		parenthesizedExpressionNamed:     parenNamed,
		binaryExpression:                 binarySym,
		binaryExpressionNamed:            binaryNamed,
		compositeValueDecomposition:      compositeSym,
		compositeValueDecompositionNamed: compositeNamed,
	}, true
}

func wgslBuildRecoveredCallAssignment(n *Node, lang *Language, syms wgslRecoveredCallAssignmentSymbols) (*Node, *Node, bool) {
	if n == nil || !n.IsError() || resultChildCount(n) != 3 {
		return nil, nil, false
	}
	callee := resultChildAt(n, 0)
	parenLHS := resultChildAt(n, 1)
	semi := resultChildAt(n, 2)
	if callee == nil || callee.Type(lang) != "identifier" ||
		parenLHS == nil || parenLHS.Type(lang) != "lhs_expression" || resultChildCount(parenLHS) != 4 ||
		semi == nil || semi.Type(lang) != ";" ||
		callee.EndByte() != parenLHS.StartByte() || parenLHS.EndByte() != semi.StartByte() {
		return nil, nil, false
	}
	open := resultChildAt(parenLHS, 0)
	argErr := resultChildAt(parenLHS, 1)
	tailLHS := resultChildAt(parenLHS, 2)
	close := resultChildAt(parenLHS, 3)
	if open == nil || open.Type(lang) != "(" ||
		argErr == nil || !argErr.IsError() || resultChildCount(argErr) != 3 ||
		tailLHS == nil || tailLHS.Type(lang) != "lhs_expression" || resultChildCount(tailLHS) != 2 ||
		close == nil || close.Type(lang) != ")" ||
		open.StartByte() != callee.EndByte() || close.EndByte() != parenLHS.EndByte() {
		return nil, nil, false
	}
	leftComposite, rightErr, ok := wgslBuildRecoveredCallBinaryLeft(argErr, lang, syms)
	if !ok {
		return nil, nil, false
	}
	star := resultChildAt(tailLHS, 0)
	right := resultChildAt(tailLHS, 1)
	if star == nil || star.Type(lang) != "*" || right == nil || right.Type(lang) != "identifier" {
		return nil, nil, false
	}
	binary := newParentNodeInArena(n.ownerArena, syms.binaryExpression, syms.binaryExpressionNamed, []*Node{leftComposite, rightErr, star, right}, nil, 0)
	binary.setHasError(true)
	paren := newParentNodeInArena(n.ownerArena, syms.parenthesizedExpression, syms.parenthesizedExpressionNamed, []*Node{open, binary, close}, nil, 0)
	paren.setHasError(true)
	lhs := newParentNodeInArena(n.ownerArena, syms.lhsExpression, syms.lhsExpressionNamed, []*Node{callee}, nil, 0)
	plusEq := newLeafNodeInArena(n.ownerArena, syms.plusEq, syms.plusEqNamed, open.StartByte(), open.StartByte(), open.StartPoint(), open.StartPoint())
	plusEq.setMissing(true)
	plusEq.setHasError(true)
	op := newParentNodeInArena(n.ownerArena, syms.compoundAssignmentOperator, syms.compoundAssignmentOperatorNamed, []*Node{plusEq}, nil, 0)
	op.setHasError(true)
	assignment := newParentNodeInArena(n.ownerArena, syms.assignmentStatement, syms.assignmentStatementNamed, []*Node{lhs, op, paren}, nil, 0)
	assignment.setHasError(true)
	return assignment, semi, true
}

func wgslBuildRecoveredCallBinaryLeft(argErr *Node, lang *Language, syms wgslRecoveredCallAssignmentSymbols) (*Node, *Node, bool) {
	first := resultChildAt(argErr, 0)
	outerComma := resultChildAt(argErr, 1)
	outerIdent := resultChildAt(argErr, 2)
	if first == nil || first.Type(lang) != "lhs_expression" || resultChildCount(first) != 2 ||
		outerComma == nil || outerComma.Type(lang) != "," ||
		outerIdent == nil || outerIdent.Type(lang) != "identifier" {
		return nil, nil, false
	}
	base := resultChildAt(first, 0)
	postfix := resultChildAt(first, 1)
	if base == nil || base.Type(lang) != "identifier" ||
		postfix == nil || postfix.Type(lang) != "postfix_expression" || resultChildCount(postfix) != 4 {
		return nil, nil, false
	}
	dot0 := resultChildAt(postfix, 0)
	fieldErr := resultChildAt(postfix, 1)
	nextBase := resultChildAt(postfix, 2)
	nextPostfix := resultChildAt(postfix, 3)
	if dot0 == nil || dot0.Type(lang) != "." ||
		fieldErr == nil || !fieldErr.IsError() || resultChildCount(fieldErr) != 2 ||
		nextBase == nil || nextBase.Type(lang) != "identifier" ||
		nextPostfix == nil || nextPostfix.Type(lang) != "postfix_expression" || resultChildCount(nextPostfix) != 2 {
		return nil, nil, false
	}
	field := resultChildAt(fieldErr, 0)
	innerComma := resultChildAt(fieldErr, 1)
	dot1 := resultChildAt(nextPostfix, 0)
	nextField := resultChildAt(nextPostfix, 1)
	if field == nil || field.Type(lang) != "identifier" ||
		innerComma == nil || innerComma.Type(lang) != "," ||
		dot1 == nil || dot1.Type(lang) != "." ||
		nextField == nil || nextField.Type(lang) != "identifier" {
		return nil, nil, false
	}
	innerComposite := newParentNodeInArena(argErr.ownerArena, syms.compositeValueDecomposition, syms.compositeValueDecompositionNamed, []*Node{base, dot0, field}, nil, 0)
	midErr := newParentNodeInArena(argErr.ownerArena, errorSymbol, true, []*Node{innerComma, nextBase}, nil, 0)
	midErr.setExtra(true)
	midErr.setHasError(true)
	outerComposite := newParentNodeInArena(argErr.ownerArena, syms.compositeValueDecomposition, syms.compositeValueDecompositionNamed, []*Node{innerComposite, midErr, dot1, nextField}, nil, 0)
	outerComposite.setHasError(true)
	rightErr := newParentNodeInArena(argErr.ownerArena, errorSymbol, true, []*Node{outerComma, outerIdent}, nil, 0)
	rightErr.setExtra(true)
	rightErr.setHasError(true)
	return outerComposite, rightErr, true
}

func normalizeWGSLRecoveredU32CallArgument(n *Node, lang *Language) {
	if n == nil || n.Type(lang) != "parenthesized_expression" || resultChildCount(n) != 4 {
		return
	}
	typeCallSym, typeCallNamed, ok := symbolMeta(lang, "type_constructor_or_function_call_expression")
	if !ok {
		return
	}
	typeDeclSym, typeDeclNamed, ok := symbolMeta(lang, "type_declaration")
	if !ok {
		return
	}
	argListSym, argListNamed, ok := symbolMeta(lang, "argument_list_expression")
	if !ok {
		return
	}
	children := resultChildSliceForMutation(n)
	open := children[0]
	err := children[1]
	argsParen := children[2]
	close := children[3]
	if open == nil || open.Type(lang) != "(" ||
		err == nil || !err.IsError() || resultChildCount(err) != 3 ||
		argsParen == nil || argsParen.Type(lang) != "parenthesized_expression" || resultChildCount(argsParen) != 3 ||
		close == nil || close.Type(lang) != ")" {
		return
	}
	errPrefix := resultChildSliceRangeForMutation(err, 0, 2)
	callee := resultChildAt(err, 2)
	if callee == nil || callee.Type(lang) != "u32" || errPrefix[1] == nil || errPrefix[1].Type(lang) != "," ||
		callee.EndByte() != argsParen.StartByte() {
		return
	}
	trimmedErr := newParentNodeInArena(n.ownerArena, errorSymbol, true, errPrefix, nil, 0)
	trimmedErr.setExtra(true)
	trimmedErr.setHasError(true)
	typeDecl := newParentNodeInArena(n.ownerArena, typeDeclSym, typeDeclNamed, []*Node{callee}, nil, 0)
	argList := newParentNodeInArena(n.ownerArena, argListSym, argListNamed, resultChildSliceForMutation(argsParen), nil, 0)
	typeCall := newParentNodeInArena(n.ownerArena, typeCallSym, typeCallNamed, []*Node{typeDecl, argList}, nil, 0)
	replaceNodeChildrenUnfielded(n, []*Node{open, trimmedErr, typeCall, close})
	n.setHasError(true)
}

func refreshWGSLHasError(n *Node) bool {
	if n == nil {
		return false
	}
	hasErr := n.IsError()
	for i := 0; i < resultChildCount(n); i++ {
		if refreshWGSLHasError(resultChildAt(n, i)) {
			hasErr = true
		}
	}
	n.setHasError(hasErr)
	return hasErr
}
