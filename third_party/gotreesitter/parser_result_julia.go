package gotreesitter

func normalizeJuliaCompatibility(root *Node, source []byte, lang *Language) {
	normalizeJuliaRecoveredReturnRange(root, source, lang)
	normalizeJuliaMacroArgumentJuxtaposition(root, source, lang)
	normalizeJuliaSubscriptSingleRowMatrix(root, source, lang)
	normalizeJuliaTrailingCommaAssignmentTuple(root, source, lang)
	if normalizeJuliaBracketForComprehensions(root, source, lang) {
		normalizeJuliaRecoveredSourceRoot(root, source, lang)
	}
}

func normalizeJuliaTrailingCommaAssignmentTuple(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "julia" || len(source) == 0 {
		return
	}
	openTupleSym, ok := symbolByName(lang, "open_tuple")
	if !ok {
		return
	}
	operatorSym, ok := symbolByName(lang, "operator")
	if !ok {
		return
	}
	walkResultTree(root, func(tuple *Node) {
		if tuple == nil || tuple.symbol != openTupleSym || resultChildCount(tuple) < 3 {
			return
		}
		children := resultChildSliceForMutation(tuple)
		if len(children) < 3 {
			return
		}
		comma := children[len(children)-2]
		last := children[len(children)-1]
		if comma == nil || last == nil || comma.startByte+1 != comma.endByte || int(last.startByte) > len(source) || source[comma.startByte] != ',' {
			return
		}
		eq, ok := juliaSingleEqualsGap(source, comma.endByte, last.startByte)
		if !ok {
			return
		}
		operator := newLeafNodeInArena(tuple.ownerArena, operatorSym, symbolIsNamed(lang, operatorSym), eq, eq+1, advancePointByBytes(Point{}, source[:eq]), advancePointByBytes(Point{}, source[:eq+1]))
		err := newParentNodeInArena(tuple.ownerArena, errorSymbol, true, cloneNodeSliceInArena(tuple.ownerArena, []*Node{operator}), nil, 0)
		err.startByte = operator.startByte
		err.startPoint = operator.startPoint
		err.endByte = operator.endByte
		err.endPoint = operator.endPoint
		err.setHasError(true)
		out := make([]*Node, 0, len(children)+1)
		out = append(out, children[:len(children)-1]...)
		out = append(out, err, last)
		replaceNodeChildrenUnfielded(tuple, cloneNodeSliceInArena(tuple.ownerArena, out))
		tuple.setHasError(true)
		tuple.productionID = 0
	})
}

func juliaSingleEqualsGap(source []byte, start, end uint32) (uint32, bool) {
	if start >= end || int(end) > len(source) {
		return 0, false
	}
	found := uint32(0)
	for i := start; i < end; i++ {
		switch source[i] {
		case ' ', '\t':
			continue
		case '=':
			if found != 0 {
				return 0, false
			}
			found = i
		default:
			return 0, false
		}
	}
	if found == 0 {
		return 0, false
	}
	return found, true
}

func normalizeJuliaBracketForComprehensions(root *Node, source []byte, lang *Language) bool {
	if root == nil || lang == nil || lang.Name != "julia" || len(source) == 0 {
		return false
	}
	matrixSym, ok := symbolByName(lang, "matrix_expression")
	if !ok {
		return false
	}
	matrixRowSym, ok := symbolByName(lang, "matrix_row")
	if !ok {
		return false
	}
	comprehensionSym, ok := symbolByName(lang, "comprehension_expression")
	if !ok {
		return false
	}
	forClauseSym, ok := symbolByName(lang, "for_clause")
	if !ok {
		return false
	}
	forStatementSym, ok := symbolByName(lang, "for_statement")
	if !ok {
		return false
	}
	rewritten := false
	walkResultTree(root, func(matrix *Node) {
		if matrix == nil || matrix.symbol != matrixSym || resultChildCount(matrix) != 4 {
			return
		}
		children := resultChildSliceForMutation(matrix)
		if len(children) != 4 {
			return
		}
		open, exprRow, forRow, close := children[0], children[1], children[2], children[3]
		if open == nil || exprRow == nil || forRow == nil || close == nil {
			return
		}
		if exprRow.symbol != matrixRowSym || forRow.symbol != matrixRowSym || resultChildCount(exprRow) != 1 || resultChildCount(forRow) != 1 {
			return
		}
		expr := resultChildAt(exprRow, 0)
		forStmt := resultChildAt(forRow, 0)
		if expr == nil || forStmt == nil || forStmt.symbol != forStatementSym || resultChildCount(forStmt) < 2 {
			return
		}
		forTok := resultChildAt(forStmt, 0)
		binding := resultChildAt(forStmt, 1)
		if forTok == nil || binding == nil {
			return
		}
		if open.startByte+1 != open.endByte || close.startByte+1 != close.endByte || int(close.endByte) > len(source) || int(forTok.endByte) > len(source) {
			return
		}
		if source[open.startByte] != '[' || source[close.startByte] != ']' || string(source[forTok.startByte:forTok.endByte]) != "for" {
			return
		}
		if forTok.startByte != forRow.startByte || forTok.endByte > binding.startByte || binding.endByte > forRow.endByte || forRow.endByte > close.startByte {
			return
		}
		forClause := newParentNodeInArena(matrix.ownerArena, forClauseSym, true, cloneNodeSliceInArena(matrix.ownerArena, []*Node{forTok, binding}), nil, 0)
		forClause.startByte = forTok.startByte
		forClause.startPoint = forTok.startPoint
		forClause.endByte = binding.endByte
		forClause.endPoint = binding.endPoint
		forClause.setHasError(false)

		matrix.symbol = comprehensionSym
		matrix.setNamed(symbolIsNamed(lang, comprehensionSym))
		matrix.productionID = 0
		matrix.setHasError(false)
		replaceNodeChildrenUnfielded(matrix, cloneNodeSliceInArena(matrix.ownerArena, []*Node{open, expr, forClause, close}))
		rewritten = true
	})
	return rewritten
}

func normalizeJuliaRecoveredSourceRoot(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || root.symbol != errorSymbol || root.startByte != 0 || int(root.endByte) != len(source) || resultChildCount(root) == 0 {
		return
	}
	sourceFileSym, ok := symbolByName(lang, "source_file")
	if !ok {
		return
	}
	root.symbol = sourceFileSym
	root.setNamed(symbolIsNamed(lang, sourceFileSym))
	root.productionID = 0
}

func normalizeJuliaSubscriptSingleRowMatrix(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "julia" || len(source) == 0 {
		return
	}
	matrixSym, ok := symbolByName(lang, "matrix_expression")
	if !ok {
		return
	}
	matrixRowSym, ok := symbolByName(lang, "matrix_row")
	if !ok {
		return
	}
	vectorSym, ok := symbolByName(lang, "vector_expression")
	if !ok {
		return
	}
	binarySym, ok := symbolByName(lang, "binary_expression")
	if !ok {
		return
	}
	juxtapositionSym, ok := symbolByName(lang, "juxtaposition_expression")
	if !ok {
		return
	}
	integerSym, ok := symbolByName(lang, "integer_literal")
	if !ok {
		return
	}
	walkResultTree(root, func(matrix *Node) {
		if matrix == nil || matrix.symbol != matrixSym {
			return
		}
		children := resultChildSliceForMutation(matrix)
		rowIndex := 0
		var open, close *Node
		if len(children) == 3 {
			open = children[0]
			rowIndex = 1
			close = children[2]
		} else if len(children) != 1 {
			return
		}
		row := children[rowIndex]
		if row == nil || row.symbol != matrixRowSym {
			return
		}
		item, ok := juliaSingleIndexMatrixRowItem(row, source, binarySym, juxtapositionSym, integerSym)
		if !ok {
			return
		}
		if len(children) == 3 {
			if open == nil || close == nil || open.startByte+1 != open.endByte || close.startByte+1 != close.endByte || int(close.endByte) > len(source) {
				return
			}
			if source[open.startByte] != '[' || source[close.startByte] != ']' {
				return
			}
			children[rowIndex] = item
		} else {
			children[0] = item
		}
		matrix.symbol = vectorSym
		matrix.setNamed(symbolIsNamed(lang, vectorSym))
		matrix.productionID = 0
		replaceNodeChildrenUnfielded(matrix, cloneNodeSliceInArena(matrix.ownerArena, children))
	})
}

func juliaSingleIndexMatrixRowItem(row *Node, source []byte, binarySym, juxtapositionSym, integerSym Symbol) (*Node, bool) {
	if row == nil {
		return nil, false
	}
	switch resultChildCount(row) {
	case 1:
		item := resultChildAt(row, 0)
		if item == nil || row.startByte != item.startByte || row.endByte != item.endByte {
			return nil, false
		}
		return item, true
	case 2:
		first := resultChildAt(row, 0)
		second := resultChildAt(row, 1)
		if first == nil || second == nil || first.symbol != integerSym || second.symbol != binarySym || resultChildCount(second) != 3 {
			return nil, false
		}
		left := resultChildAt(second, 0)
		op := resultChildAt(second, 1)
		right := resultChildAt(second, 2)
		if left == nil || op == nil || right == nil || first.endByte != left.startByte || int(left.startByte) > len(source) {
			return nil, false
		}
		juxtaposition := newParentNodeInArena(row.ownerArena, juxtapositionSym, true, cloneNodeSliceInArena(row.ownerArena, []*Node{first, left}), nil, 0)
		replaceNodeChildrenUnfielded(second, cloneNodeSliceInArena(second.ownerArena, []*Node{juxtaposition, op, right}))
		second.productionID = 0
		return second, true
	default:
		return nil, false
	}
}

func normalizeJuliaMacroArgumentJuxtaposition(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "julia" || len(source) == 0 {
		return
	}
	argListSym, ok := symbolByName(lang, "macro_argument_list")
	if !ok {
		return
	}
	binarySym, ok := symbolByName(lang, "binary_expression")
	if !ok {
		return
	}
	juxtapositionSym, ok := symbolByName(lang, "juxtaposition_expression")
	if !ok {
		return
	}
	integerSym, ok := symbolByName(lang, "integer_literal")
	if !ok {
		return
	}
	stringSym, ok := symbolByName(lang, "string_literal")
	if !ok {
		return
	}
	walkResultTree(root, func(argList *Node) {
		if argList == nil || argList.symbol != argListSym || resultChildCount(argList) != 2 {
			return
		}
		first := resultChildAt(argList, 0)
		second := resultChildAt(argList, 1)
		switch {
		case juliaFuseLeadingMacroInteger(argList, first, second, source, binarySym, juxtapositionSym, integerSym):
		case juliaFuseTrailingMacroString(argList, first, second, source, binarySym, juxtapositionSym, integerSym, stringSym):
		}
	})
}

func juliaFuseLeadingMacroInteger(argList, first, second *Node, source []byte, binarySym, juxtapositionSym, integerSym Symbol) bool {
	if argList == nil || first == nil || second == nil || first.symbol != integerSym || second.symbol != binarySym || resultChildCount(second) != 3 {
		return false
	}
	left := resultChildAt(second, 0)
	op := resultChildAt(second, 1)
	right := resultChildAt(second, 2)
	if left == nil || op == nil || right == nil {
		return false
	}
	if first.endByte != left.startByte || int(left.startByte) > len(source) {
		return false
	}
	juxtaposition := newParentNodeInArena(argList.ownerArena, juxtapositionSym, true, cloneNodeSliceInArena(argList.ownerArena, []*Node{first, left}), nil, 0)
	replaceNodeChildrenUnfielded(second, cloneNodeSliceInArena(second.ownerArena, []*Node{juxtaposition, op, right}))
	second.productionID = 0
	replaceNodeChildrenUnfielded(argList, cloneNodeSliceInArena(argList.ownerArena, []*Node{second}))
	return true
}

func juliaFuseTrailingMacroString(argList, first, second *Node, source []byte, binarySym, juxtapositionSym, integerSym, stringSym Symbol) bool {
	if argList == nil || first == nil || second == nil || first.symbol != binarySym || second.symbol != stringSym {
		return false
	}
	if !juliaFuseTrailingMacroStringInBinary(first, second, source, binarySym, juxtapositionSym, integerSym) {
		return false
	}
	replaceNodeChildrenUnfielded(argList, cloneNodeSliceInArena(argList.ownerArena, []*Node{first}))
	return true
}

func juliaFuseTrailingMacroStringInBinary(binary, str *Node, source []byte, binarySym, juxtapositionSym, integerSym Symbol) bool {
	if binary == nil || str == nil || binary.symbol != binarySym || resultChildCount(binary) != 3 {
		return false
	}
	children := resultChildSliceForMutation(binary)
	if len(children) != 3 {
		return false
	}
	right := children[2]
	if right == nil {
		return false
	}
	if right.symbol == binarySym && juliaFuseTrailingMacroStringInBinary(right, str, source, binarySym, juxtapositionSym, integerSym) {
		replaceNodeChildrenUnfielded(binary, cloneNodeSliceInArena(binary.ownerArena, children))
		binary.productionID = 0
		return true
	}
	if right.symbol != integerSym || !juliaMacroWhitespaceGap(source, right.endByte, str.startByte) {
		return false
	}
	juxtaposition := newParentNodeInArena(binary.ownerArena, juxtapositionSym, true, cloneNodeSliceInArena(binary.ownerArena, []*Node{right, str}), nil, 0)
	children[2] = juxtaposition
	replaceNodeChildrenUnfielded(binary, cloneNodeSliceInArena(binary.ownerArena, children))
	binary.productionID = 0
	return true
}

func juliaMacroWhitespaceGap(source []byte, start, end uint32) bool {
	if start >= end || int(end) > len(source) {
		return false
	}
	for _, b := range source[start:end] {
		if b != ' ' && b != '\t' {
			return false
		}
	}
	return true
}

func normalizeJuliaRecoveredReturnRange(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "julia" || len(source) == 0 {
		return
	}
	blockSym, ok := symbolByName(lang, "block")
	if !ok {
		return
	}
	returnSym, ok := symbolByName(lang, "return_statement")
	if !ok {
		return
	}
	rangeSym, ok := symbolByName(lang, "range_expression")
	if !ok {
		return
	}
	quoteSym, ok := symbolByName(lang, "quote_expression")
	if !ok {
		return
	}
	binarySym, ok := symbolByName(lang, "binary_expression")
	if !ok {
		return
	}
	parenSym, ok := symbolByName(lang, "parenthesized_expression")
	if !ok {
		return
	}
	integerSym, ok := symbolByName(lang, "integer_literal")
	if !ok {
		return
	}
	walkResultTree(root, func(block *Node) {
		if block == nil || block.symbol != blockSym || resultChildCount(block) == 0 {
			return
		}
		children := resultChildSliceForMutation(block)
		blockEndByte := block.endByte
		blockEndPoint := block.endPoint
		rewritten := false
		out := make([]*Node, 0, len(children)+1)
		for _, child := range children {
			out = append(out, child)
			closeParen, ok := juliaRewriteReturnRange(child, source, returnSym, rangeSym, quoteSym, binarySym, parenSym, integerSym)
			if !ok {
				continue
			}
			if closeParen != nil {
				closeErr := newParentNodeInArena(block.ownerArena, errorSymbol, true, []*Node{closeParen}, nil, 0)
				closeErr.startByte = closeParen.startByte
				closeErr.startPoint = closeParen.startPoint
				closeErr.endByte = closeParen.endByte
				closeErr.endPoint = closeParen.endPoint
				closeErr.setHasError(true)
				closeErr.setExtra(true)
				out = append(out, closeErr)
			}
			rewritten = ok || rewritten
		}
		if rewritten {
			replaceNodeChildrenUnfielded(block, cloneNodeSliceInArena(block.ownerArena, out))
			block.endByte = blockEndByte
			block.endPoint = blockEndPoint
			block.setHasError(true)
			root.setHasError(true)
		}
	})
}

func juliaRewriteReturnRange(ret *Node, source []byte, returnSym, rangeSym, quoteSym, binarySym, parenSym, integerSym Symbol) (*Node, bool) {
	if ret == nil || ret.symbol != returnSym || resultChildCount(ret) != 2 {
		return nil, false
	}
	expr := resultChildAt(ret, 1)
	if expr == nil || expr.symbol != rangeSym || resultChildCount(expr) != 3 {
		return nil, false
	}
	left := resultChildAt(expr, 0)
	colon := resultChildAt(expr, 1)
	right := resultChildAt(expr, 2)
	if juliaRewriteBareReturnRange(ret, expr, left, colon, right, source, quoteSym) {
		return nil, true
	}
	if left == nil || colon == nil || right == nil || right.symbol != parenSym || resultChildCount(right) != 3 {
		return nil, false
	}
	open := resultChildAt(right, 0)
	inner := resultChildAt(right, 1)
	closeParen := resultChildAt(right, 2)
	if open == nil || inner == nil || closeParen == nil || inner.symbol != binarySym || resultChildCount(inner) != 3 {
		return nil, false
	}
	innerLeft := resultChildAt(inner, 0)
	innerOp := resultChildAt(inner, 1)
	innerRight := resultChildAt(inner, 2)
	if innerLeft == nil || innerOp == nil || innerRight == nil || innerRight.symbol != integerSym {
		return nil, false
	}
	if colon.startByte+1 != colon.endByte || open.startByte+1 != open.endByte || closeParen.startByte+1 != closeParen.endByte {
		return nil, false
	}
	if int(closeParen.endByte) > len(source) ||
		source[colon.startByte] != ':' ||
		source[open.startByte] != '(' ||
		source[closeParen.startByte] != ')' {
		return nil, false
	}
	if colon.endByte > open.startByte || open.endByte > innerLeft.startByte || innerLeft.endByte > innerOp.startByte {
		return nil, false
	}
	err := newParentNodeInArena(expr.ownerArena, errorSymbol, true, []*Node{colon, open}, nil, 0)
	err.startByte = colon.startByte
	err.startPoint = colon.startPoint
	err.endByte = innerLeft.endByte
	err.endPoint = innerLeft.endPoint
	err.setHasError(true)
	err.setExtra(true)

	expr.symbol = binarySym
	expr.setNamed(true)
	expr.children = cloneNodeSliceInArena(expr.ownerArena, []*Node{left, err, innerOp, innerRight})
	expr.clearFieldMetadata()
	expr.productionID = 0
	expr.endByte = innerRight.endByte
	expr.endPoint = innerRight.endPoint
	expr.setHasError(true)
	populateParentNode(expr, expr.children)

	ret.endByte = expr.endByte
	ret.endPoint = expr.endPoint
	ret.setHasError(true)
	return closeParen, true
}

func juliaRewriteBareReturnRange(ret, expr, left, colon, right *Node, source []byte, quoteSym Symbol) bool {
	if ret == nil || expr == nil || left == nil || colon == nil || right == nil {
		return false
	}
	if resultChildCount(left) != 0 || resultChildCount(right) != 0 {
		return false
	}
	if colon.startByte+1 != colon.endByte || int(colon.endByte) > len(source) || source[colon.startByte] != ':' {
		return false
	}
	if left.endByte > colon.startByte || colon.endByte > right.startByte {
		return false
	}
	err := newLeafNodeInArena(expr.ownerArena, errorSymbol, true, left.startByte, left.endByte, left.startPoint, left.endPoint)
	err.setHasError(true)
	err.setExtra(true)

	expr.symbol = quoteSym
	expr.setNamed(true)
	expr.startByte = colon.startByte
	expr.startPoint = colon.startPoint
	expr.children = cloneNodeSliceInArena(expr.ownerArena, []*Node{colon, right})
	expr.clearFieldMetadata()
	expr.productionID = 0
	expr.setHasError(false)
	populateParentNode(expr, expr.children)

	ret.children = cloneNodeSliceInArena(ret.ownerArena, []*Node{resultChildAt(ret, 0), err, expr})
	ret.clearFieldMetadata()
	ret.setHasError(true)
	populateParentNode(ret, ret.children)
	return true
}
