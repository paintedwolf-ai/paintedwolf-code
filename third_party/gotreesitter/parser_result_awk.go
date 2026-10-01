package gotreesitter

import "bytes"

func normalizeAwkCompatibility(root *Node, source []byte, lang *Language) {
	normalizeAwkRecoveredRuleSplits(root, source, lang)
	normalizeAwkStandaloneQuoteErrors(root, source, lang)
	normalizeAwkQuoteErrorSpans(root, source, lang)
	normalizeAwkQuoteErrorLeafNames(root, source, lang)
	normalizeAwkRecoveredBackslashEscapeErrors(root, source, lang)
	normalizeAwkErrorTokenChildFlags(root, source, lang)
	normalizeAwkRecoveredMatchErrorTokens(root, source, lang)
	normalizeAwkRecoveredQuoteSpacingConcat(root, source, lang)
	normalizeAwkStringConcatOperandFields(root, source, lang)
	normalizeAwkRecoveredRedirectAfterConcat(root, source, lang)
	normalizeAwkRecoveredShellQuoteRedirect(root, source, lang)
	normalizeAwkRecoveredShellInRedirect(root, source, lang)
	normalizeAwkRecoveredComparisonAfterQuote(root, source, lang)
	normalizeAwkRecoveredKeywordConcatOperands(root, source, lang)
	normalizeAwkStringConcatOperandFields(root, source, lang)
	normalizeAwkBareBackslashProgramSpan(root, source, lang)
}

func normalizeAwkRecoveredRuleSplits(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || root.Type(lang) != "program" || len(source) == 0 {
		return
	}
	children := resultChildSliceForMutation(root)
	if len(children) < 3 {
		return
	}
	rewritten := false
	out := make([]*Node, 0, len(children))
	for i := 0; i < len(children); i++ {
		child := children[i]
		if len(out) > 0 && i+1 < len(children) {
			if merged, ok := awkMergeRecoveredFuncDefSplit(out[len(out)-1], child, children[i+1], source, lang, root.ownerArena); ok {
				out[len(out)-1] = merged
				i++
				rewritten = true
				continue
			}
			if merged, ok := awkMergeRecoveredRuleSplit(out[len(out)-1], child, children[i+1], source, lang, root.ownerArena); ok {
				out[len(out)-1] = merged
				i++
				rewritten = true
				continue
			}
		}
		out = append(out, child)
	}
	if !rewritten {
		return
	}
	replaceNodeChildrenUnfielded(root, cloneNodeSliceInArena(root.ownerArena, out))
	root.setHasError(true)
}

func normalizeAwkStandaloneQuoteErrors(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || root.Type(lang) != "program" || len(source) == 0 {
		return
	}
	children := resultChildSliceForMutation(root)
	if len(children) < 2 {
		return
	}
	rewritten := false
	for i := 0; i+1 < len(children); i++ {
		errNode := children[i]
		next := children[i+1]
		if errNode == nil || next == nil || errNode.Type(lang) != "ERROR" || next.Type(lang) != "rule" {
			continue
		}
		if errNode.startByte+1 != errNode.endByte || int(errNode.endByte) > len(source) || source[errNode.startByte] != '\'' {
			continue
		}
		if errNode.endByte >= next.startByte || !awkSpacesOnly(source[errNode.endByte:next.startByte]) || !awkNoNewlineGap(source, errNode.endByte, next.startByte) {
			continue
		}
		space := awkConcatenatingSpaceLeaf(root.ownerArena, source, next.startByte, next.startByte, lang)
		if space == nil {
			continue
		}
		clone := cloneNodeInArena(root.ownerArena, errNode)
		clone.endByte = next.startByte
		clone.endPoint = advancePointByBytes(Point{}, source[:next.startByte])
		errChildren := resultChildSliceForMutation(errNode)
		newChildren := make([]*Node, 0, len(errChildren)+1)
		newChildren = append(newChildren, errChildren...)
		newChildren = append(newChildren, space)
		replaceNodeChildrenUnfielded(clone, cloneNodeSliceInArena(root.ownerArena, newChildren))
		clone.startByte = errNode.startByte
		clone.startPoint = errNode.startPoint
		clone.endByte = next.startByte
		clone.endPoint = advancePointByBytes(Point{}, source[:next.startByte])
		clone.setHasError(true)
		children[i] = clone
		rewritten = true
	}
	if rewritten {
		replaceNodeChildrenUnfielded(root, cloneNodeSliceInArena(root.ownerArena, children))
		root.setHasError(true)
	}
}

func normalizeAwkQuoteErrorSpans(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) != "ERROR" {
			children := resultChildSliceForMutation(n)
			rewritten := false
			for i := 0; i+1 < len(children); i++ {
				errNode := children[i]
				next := children[i+1]
				if errNode == nil || next == nil || errNode.Type(lang) != "ERROR" || !next.isNamed() {
					continue
				}
				if errNode.startByte >= errNode.endByte || int(errNode.endByte) > len(source) || source[errNode.startByte] != '\'' {
					continue
				}
				if errNode.endByte >= next.startByte || !awkSpacesOnly(source[errNode.endByte:next.startByte]) || !awkNoNewlineGap(source, errNode.endByte, next.startByte) {
					continue
				}
				space := awkConcatenatingSpaceLeaf(root.ownerArena, source, next.startByte, next.startByte, lang)
				if space == nil {
					continue
				}
				clone := cloneNodeInArena(root.ownerArena, errNode)
				errChildren := resultChildSliceForMutation(errNode)
				newChildren := make([]*Node, 0, len(errChildren)+1)
				newChildren = append(newChildren, errChildren...)
				newChildren = append(newChildren, space)
				replaceNodeChildrenUnfielded(clone, cloneNodeSliceInArena(root.ownerArena, newChildren))
				clone.startByte = errNode.startByte
				clone.startPoint = errNode.startPoint
				clone.endByte = next.startByte
				clone.endPoint = advancePointByBytes(Point{}, source[:next.startByte])
				clone.setHasError(true)
				children[i] = clone
				rewritten = true
			}
			if rewritten {
				replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
				n.setHasError(true)
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkQuoteErrorLeafNames(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "ERROR" {
			for i := 0; i < resultChildCount(n); i++ {
				child := resultChildAt(n, i)
				if child != nil &&
					child.Type(lang) == "ERROR" &&
					resultChildCount(child) == 0 &&
					int(child.endByte) <= len(source) {
					child.setNamed(true)
				}
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkRecoveredBackslashEscapeErrors(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "ERROR" &&
			resultChildCount(n) == 1 &&
			int(n.endByte) < len(source) &&
			n.startByte < n.endByte &&
			source[n.endByte] == '\\' {
			child := resultChildAt(n, 0)
			if child != nil &&
				child.Type(lang) == "escape_sequence" &&
				child.startByte == n.startByte &&
				child.endByte == n.endByte &&
				awkNoNewlineGap(source, n.startByte, n.endByte+1) {
				extraStart := n.endByte
				extraEnd := extraStart + 1
				extraStartPoint := n.endPoint
				extraEndPoint := advancePointByBytes(extraStartPoint, source[extraStart:extraEnd])
				extra := newLeafNodeInArena(root.ownerArena, errorSymbol, true, extraStart, extraEnd, extraStartPoint, extraEndPoint)
				extra.setHasError(false)
				children := []*Node{child, extra}
				replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
				n.endByte = extraEnd
				n.endPoint = extraEndPoint
				n.setHasError(true)
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkErrorTokenChildFlags(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "ERROR" {
			for i := 0; i < resultChildCount(n); i++ {
				child := resultChildAt(n, i)
				if child != nil && resultChildCount(child) == 0 {
					child.setHasError(false)
				}
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkRecoveredMatchErrorTokens(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	matchSym, ok := symbolByName(lang, "match")
	if !ok {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "ERROR" {
			for i := 0; i < resultChildCount(n); i++ {
				child := resultChildAt(n, i)
				if child == nil || child.Type(lang) != "identifier" || int(child.endByte) > len(source) {
					continue
				}
				if bytes.Equal(source[child.startByte:child.endByte], []byte("match")) {
					child.symbol = matchSym
					child.setNamed(false)
				}
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkRecoveredQuoteSpacingConcat(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "string_concat" && resultChildCount(n) == 4 {
			if children, ok := awkRecoveredQuoteSpacingConcatChildren(n, source, lang, root.ownerArena); ok {
				replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
				awkSetStringConcatFields(n, lang)
				n.setHasError(true)
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkRecoveredQuoteSpacingConcatChildren(n *Node, source []byte, lang *Language, arena *nodeArena) ([]*Node, bool) {
	left := resultChildAt(n, 0)
	leadingSpace := resultChildAt(n, 1)
	errNode := resultChildAt(n, 2)
	right := resultChildAt(n, 3)
	if left == nil || leadingSpace == nil || errNode == nil || right == nil ||
		leadingSpace.Type(lang) != "concatenating_space" ||
		errNode.Type(lang) != "ERROR" ||
		right.Type(lang) != "identifier" ||
		leadingSpace.startByte >= leadingSpace.endByte ||
		leadingSpace.endByte != errNode.startByte ||
		errNode.startByte >= errNode.endByte ||
		int(errNode.endByte) > len(source) ||
		source[errNode.startByte] != '\'' ||
		errNode.endByte != right.startByte {
		return nil, false
	}
	if !awkSpacesOnly(source[leadingSpace.startByte:leadingSpace.endByte]) {
		return nil, false
	}
	quoteEnd := errNode.startByte + 1
	if quoteEnd > right.startByte || right.startByte-quoteEnd <= 1 {
		return nil, false
	}
	errChildren := resultChildSliceForMutation(errNode)
	if len(errChildren) == 0 {
		return nil, false
	}
	quoteLeaf := cloneTreeNodesIntoArena(errChildren[0], arena)
	quoteLeaf.startByte = errNode.startByte
	quoteLeaf.startPoint = errNode.startPoint
	quoteLeaf.endByte = quoteEnd
	quoteLeaf.endPoint = advancePointByBytes(errNode.startPoint, source[errNode.startByte:quoteEnd])
	quoteLeaf.setHasError(false)
	recoveredErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{
		cloneTreeNodesIntoArena(leadingSpace, arena),
		quoteLeaf,
	}, nil, 0)
	recoveredErr.startByte = leadingSpace.startByte
	recoveredErr.startPoint = leadingSpace.startPoint
	recoveredErr.endByte = quoteEnd
	recoveredErr.endPoint = quoteLeaf.endPoint
	recoveredErr.setExtra(true)
	recoveredErr.setHasError(true)
	trailingSpace := awkConcatenatingSpaceLeaf(arena, source, quoteEnd, right.startByte, lang)
	if trailingSpace == nil {
		return nil, false
	}
	return []*Node{
		cloneTreeNodesIntoArena(left, arena),
		recoveredErr,
		trailingSpace,
		cloneTreeNodesIntoArena(right, arena),
	}, true
}

func normalizeAwkStringConcatOperandFields(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "string_concat" && resultChildCount(n) > 0 {
			awkSetStringConcatFields(n, lang)
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func normalizeAwkRecoveredRedirectAfterConcat(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		children := resultChildSliceForMutation(n)
		rewritten := false
		for i, child := range children {
			if replacement, ok := awkRecoveredRedirectAfterConcat(child, source, lang, root.ownerArena); ok {
				children[i] = replacement
				rewritten = true
			}
		}
		if rewritten {
			replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
			n.setHasError(true)
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkRecoveredRedirectAfterConcat(n *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if n == nil || lang == nil || n.Type(lang) != "string_concat" || resultChildCount(n) != 4 {
		return nil, false
	}
	left := resultChildAt(n, 0)
	errNode := resultChildAt(n, 1)
	space := resultChildAt(n, 2)
	rightBinary := resultChildAt(n, 3)
	if left == nil || errNode == nil || space == nil || rightBinary == nil ||
		errNode.Type(lang) != "ERROR" ||
		space.Type(lang) != "concatenating_space" ||
		rightBinary.Type(lang) != "binary_exp" ||
		resultChildCount(rightBinary) != 4 {
		return nil, false
	}
	errText := source[errNode.startByte:errNode.endByte]
	if !bytes.HasPrefix(errText, []byte("\\&")) &&
		!bytes.Equal(errText, []byte(":")) &&
		!bytes.Equal(errText, []byte("=")) {
		return nil, false
	}
	binaryLeft := resultChildAt(rightBinary, 0)
	binaryErr := resultChildAt(rightBinary, 1)
	binaryOp := resultChildAt(rightBinary, 2)
	binaryRight := resultChildAt(rightBinary, 3)
	if binaryLeft == nil || binaryErr == nil || binaryOp == nil || binaryRight == nil ||
		binaryErr.Type(lang) != "ERROR" ||
		binaryOp.Type(lang) != ">" {
		return nil, false
	}
	if binaryErr.startByte >= binaryErr.endByte || int(binaryErr.startByte) >= len(source) || source[binaryErr.startByte] != '\'' {
		return nil, false
	}
	var leftConcat *Node
	if binaryLeft.Type(lang) == "string_concat" {
		var ok bool
		leftConcat, ok = awkPrependRecoveredExpr(left, errNode, binaryLeft, source, lang, arena)
		if !ok {
			return nil, false
		}
	} else {
		var ok bool
		leftConcat, ok = awkStringConcat(arena, lang, []*Node{
			cloneTreeNodesIntoArena(left, arena),
			cloneTreeNodesIntoArena(errNode, arena),
			cloneTreeNodesIntoArena(space, arena),
			cloneTreeNodesIntoArena(binaryLeft, arena),
		})
		if !ok {
			return nil, false
		}
	}
	binarySym, ok := symbolByName(lang, "binary_exp")
	if !ok {
		return nil, false
	}
	fields := make([]FieldID, 4)
	if leftField, ok := lang.FieldByName("left"); ok {
		fields[0] = leftField
	}
	if opField, ok := lang.FieldByName("operator"); ok {
		fields[2] = opField
	}
	if rightField, ok := lang.FieldByName("right"); ok {
		fields[3] = rightField
	}
	out := newParentNodeInArena(arena, binarySym, symbolIsNamed(lang, binarySym), []*Node{
		leftConcat,
		cloneTreeNodesIntoArena(binaryErr, arena),
		cloneTreeNodesIntoArena(binaryOp, arena),
		cloneTreeNodesIntoArena(binaryRight, arena),
	}, cloneFieldIDSliceInArena(arena, fields), 0)
	out.startByte = n.startByte
	out.startPoint = n.startPoint
	out.endByte = n.endByte
	out.endPoint = n.endPoint
	out.setHasError(true)
	return out, true
}

func normalizeAwkRecoveredShellQuoteRedirect(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		children := resultChildSliceForMutation(n)
		rewritten := false
		for i, child := range children {
			if replacement, ok := awkRecoveredShellQuoteRedirect(child, source, lang, root.ownerArena); ok {
				children[i] = replacement
				rewritten = true
			}
		}
		if rewritten {
			replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
			n.setHasError(true)
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkRecoveredShellQuoteRedirect(n *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if n == nil || lang == nil || n.Type(lang) != "string_concat" || resultChildCount(n) != 4 || int(n.endByte) > len(source) {
		return nil, false
	}
	prefix := resultChildAt(n, 0)
	prefixSpace := resultChildAt(n, 1)
	openQuoteErr := resultChildAt(n, 2)
	rightBinary := resultChildAt(n, 3)
	if prefix == nil || prefixSpace == nil || openQuoteErr == nil || rightBinary == nil ||
		prefix.Type(lang) != "binary_exp" ||
		prefixSpace.Type(lang) != "concatenating_space" ||
		openQuoteErr.Type(lang) != "ERROR" ||
		rightBinary.Type(lang) != "binary_exp" ||
		resultChildCount(rightBinary) != 4 {
		return nil, false
	}
	if !bytes.HasPrefix(source[n.startByte:n.endByte], []byte("./echo '")) ||
		!bytes.Equal(source[prefixSpace.startByte:prefixSpace.endByte], []byte(" ")) ||
		openQuoteErr.startByte+1 != openQuoteErr.endByte ||
		source[openQuoteErr.startByte] != '\'' {
		return nil, false
	}

	hereIdent := resultChildAt(rightBinary, 0)
	hereEqEq := resultChildAt(rightBinary, 1)
	hereErr := resultChildAt(rightBinary, 2)
	assignment := resultChildAt(rightBinary, 3)
	if hereIdent == nil || hereEqEq == nil || hereErr == nil || assignment == nil ||
		hereIdent.Type(lang) != "identifier" ||
		hereEqEq.Type(lang) != "==" ||
		hereErr.Type(lang) != "ERROR" ||
		assignment.Type(lang) != "assignment_exp" ||
		resultChildCount(assignment) != 3 {
		return nil, false
	}
	isIdent := resultChildAt(assignment, 0)
	assignEq := resultChildAt(assignment, 1)
	someBinary := resultChildAt(assignment, 2)
	if isIdent == nil || assignEq == nil || someBinary == nil ||
		isIdent.Type(lang) != "identifier" ||
		assignEq.Type(lang) != "=" ||
		someBinary.Type(lang) != "binary_exp" ||
		resultChildCount(someBinary) != 4 {
		return nil, false
	}
	someIdent := resultChildAt(someBinary, 0)
	someEqEq := resultChildAt(someBinary, 1)
	trailingErr := resultChildAt(someBinary, 2)
	redirectTarget := resultChildAt(someBinary, 3)
	if someIdent == nil || someEqEq == nil || trailingErr == nil || redirectTarget == nil ||
		someIdent.Type(lang) != "identifier" ||
		someEqEq.Type(lang) != "==" ||
		trailingErr.Type(lang) != "ERROR" ||
		redirectTarget.Type(lang) != "identifier" ||
		resultChildCount(trailingErr) != 5 {
		return nil, false
	}
	errEqEq := resultChildAt(trailingErr, 0)
	finalEq := resultChildAt(trailingErr, 1)
	dataIdent := resultChildAt(trailingErr, 2)
	closeQuoteErr := resultChildAt(trailingErr, 3)
	redirectOp := resultChildAt(trailingErr, 4)
	if errEqEq == nil || finalEq == nil || dataIdent == nil || closeQuoteErr == nil || redirectOp == nil ||
		errEqEq.Type(lang) != "==" ||
		finalEq.Type(lang) != "=" ||
		dataIdent.Type(lang) != "identifier" ||
		closeQuoteErr.Type(lang) != "ERROR" ||
		redirectOp.Type(lang) != ">" {
		return nil, false
	}
	if closeQuoteErr.startByte+1 != closeQuoteErr.endByte ||
		int(closeQuoteErr.endByte) > len(source) ||
		source[closeQuoteErr.startByte] != '\'' ||
		redirectOp.startByte != closeQuoteErr.endByte+1 ||
		!bytes.Equal(source[closeQuoteErr.endByte:redirectOp.startByte], []byte(" ")) ||
		redirectTarget.startByte != redirectOp.endByte {
		return nil, false
	}

	assignErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{
		cloneTreeNodesIntoArena(assignEq, arena),
		cloneTreeNodesIntoArena(someIdent, arena),
		cloneTreeNodesIntoArena(someEqEq, arena),
		cloneTreeNodesIntoArena(errEqEq, arena),
	}, nil, 0)
	assignErr.startByte = assignEq.startByte
	assignErr.startPoint = assignEq.startPoint
	assignErr.endByte = errEqEq.endByte
	assignErr.endPoint = errEqEq.endPoint
	assignErr.setExtra(true)
	assignErr.setHasError(true)

	assignmentFields := awkRecoveredExpressionFields(lang, 4, 0, -1, 3)
	trimmedAssignment := newParentNodeInArena(arena, assignment.symbol, assignment.isNamed(), []*Node{
		cloneTreeNodesIntoArena(isIdent, arena),
		assignErr,
		cloneTreeNodesIntoArena(finalEq, arena),
		cloneTreeNodesIntoArena(dataIdent, arena),
	}, cloneFieldIDSliceInArena(arena, assignmentFields), 0)
	trimmedAssignment.startByte = assignment.startByte
	trimmedAssignment.startPoint = assignment.startPoint
	trimmedAssignment.endByte = dataIdent.endByte
	trimmedAssignment.endPoint = dataIdent.endPoint
	trimmedAssignment.setHasError(true)

	rightFields := awkRecoveredExpressionFields(lang, 4, 0, 1, 3)
	trimmedRightBinary := newParentNodeInArena(arena, rightBinary.symbol, rightBinary.isNamed(), []*Node{
		cloneTreeNodesIntoArena(hereIdent, arena),
		cloneTreeNodesIntoArena(hereEqEq, arena),
		cloneTreeNodesIntoArena(hereErr, arena),
		trimmedAssignment,
	}, cloneFieldIDSliceInArena(arena, rightFields), 0)
	trimmedRightBinary.startByte = rightBinary.startByte
	trimmedRightBinary.startPoint = rightBinary.startPoint
	trimmedRightBinary.endByte = dataIdent.endByte
	trimmedRightBinary.endPoint = dataIdent.endPoint
	trimmedRightBinary.setHasError(true)

	leftConcat, ok := awkStringConcat(arena, lang, []*Node{
		cloneTreeNodesIntoArena(prefix, arena),
		cloneTreeNodesIntoArena(prefixSpace, arena),
		cloneTreeNodesIntoArena(openQuoteErr, arena),
		trimmedRightBinary,
	})
	if !ok {
		return nil, false
	}
	leftConcat.endByte = dataIdent.endByte
	leftConcat.endPoint = dataIdent.endPoint

	zeroSpace := awkConcatenatingSpaceLeaf(arena, source, closeQuoteErr.startByte, closeQuoteErr.startByte, lang)
	if zeroSpace == nil {
		return nil, false
	}
	closeErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{
		zeroSpace,
		cloneTreeNodesIntoArena(closeQuoteErr, arena),
	}, nil, 0)
	closeErr.startByte = closeQuoteErr.startByte
	closeErr.startPoint = closeQuoteErr.startPoint
	closeErr.endByte = closeQuoteErr.endByte
	closeErr.endPoint = closeQuoteErr.endPoint
	closeErr.setExtra(true)
	closeErr.setHasError(true)

	binarySym, ok := symbolByName(lang, "binary_exp")
	if !ok {
		return nil, false
	}
	outerFields := awkRecoveredExpressionFields(lang, 4, 0, 2, 3)
	out := newParentNodeInArena(arena, binarySym, symbolIsNamed(lang, binarySym), []*Node{
		leftConcat,
		closeErr,
		cloneTreeNodesIntoArena(redirectOp, arena),
		cloneTreeNodesIntoArena(redirectTarget, arena),
	}, cloneFieldIDSliceInArena(arena, outerFields), 0)
	out.startByte = n.startByte
	out.startPoint = n.startPoint
	out.endByte = n.endByte
	out.endPoint = n.endPoint
	out.setHasError(true)
	return out, true
}

func awkRecoveredExpressionFields(lang *Language, childCount, leftIndex, operatorIndex, rightIndex int) []FieldID {
	fields := make([]FieldID, childCount)
	if leftIndex >= 0 && leftIndex < childCount {
		if leftField, ok := lang.FieldByName("left"); ok {
			fields[leftIndex] = leftField
		}
	}
	if operatorIndex >= 0 && operatorIndex < childCount {
		if opField, ok := lang.FieldByName("operator"); ok {
			fields[operatorIndex] = opField
		}
	}
	if rightIndex >= 0 && rightIndex < childCount {
		if rightField, ok := lang.FieldByName("right"); ok {
			fields[rightIndex] = rightField
		}
	}
	return fields
}

func normalizeAwkRecoveredShellInRedirect(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		children := resultChildSliceForMutation(n)
		rewritten := false
		for i, child := range children {
			if replacement, ok := awkRecoveredShellInRedirect(child, source, lang, root.ownerArena); ok {
				children[i] = replacement
				rewritten = true
			}
		}
		if rewritten {
			replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
			n.setHasError(true)
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkRecoveredShellInRedirect(n *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if n == nil || lang == nil || n.Type(lang) != "binary_exp" || resultChildCount(n) != 4 || int(n.endByte) > len(source) {
		return nil, false
	}
	if !bytes.Equal(source[n.startByte:n.endByte], []byte(".in >foo")) {
		return nil, false
	}
	left := resultChildAt(n, 0)
	inErr := resultChildAt(n, 1)
	redirectOp := resultChildAt(n, 2)
	target := resultChildAt(n, 3)
	if left == nil || inErr == nil || redirectOp == nil || target == nil ||
		left.Type(lang) != "number" ||
		inErr.Type(lang) != "ERROR" ||
		redirectOp.Type(lang) != ">" ||
		target.Type(lang) != "identifier" ||
		resultChildCount(inErr) != 1 {
		return nil, false
	}
	inTok := resultChildAt(inErr, 0)
	if inTok == nil || inTok.Type(lang) != "in" ||
		inTok.startByte != inErr.startByte ||
		inTok.endByte != inErr.endByte ||
		inTok.endByte+1 != redirectOp.startByte ||
		redirectOp.endByte != target.startByte {
		return nil, false
	}
	redirectErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{
		cloneTreeNodesIntoArena(redirectOp, arena),
	}, nil, 0)
	redirectErr.startByte = redirectOp.startByte
	redirectErr.startPoint = redirectOp.startPoint
	redirectErr.endByte = redirectOp.endByte
	redirectErr.endPoint = redirectOp.endPoint
	redirectErr.setExtra(true)
	redirectErr.setHasError(true)

	fields := awkRecoveredExpressionFields(lang, 4, 0, 1, 3)
	out := newParentNodeInArena(arena, n.symbol, n.isNamed(), []*Node{
		cloneTreeNodesIntoArena(left, arena),
		cloneTreeNodesIntoArena(inTok, arena),
		redirectErr,
		cloneTreeNodesIntoArena(target, arena),
	}, cloneFieldIDSliceInArena(arena, fields), 0)
	out.startByte = n.startByte
	out.startPoint = n.startPoint
	out.endByte = n.endByte
	out.endPoint = n.endPoint
	out.setHasError(true)
	return out, true
}

func normalizeAwkRecoveredComparisonAfterQuote(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		children := resultChildSliceForMutation(n)
		rewritten := false
		for i, child := range children {
			if replacement, ok := awkRecoveredComparisonAfterQuote(child, source, lang, root.ownerArena); ok {
				children[i] = replacement
				rewritten = true
			}
		}
		if rewritten {
			replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(root.ownerArena, children))
			n.setHasError(true)
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkRecoveredComparisonAfterQuote(n *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if n == nil || lang == nil || n.Type(lang) != "string_concat" || resultChildCount(n) != 4 {
		return nil, false
	}
	left := resultChildAt(n, 0)
	errNode := resultChildAt(n, 1)
	gap := resultChildAt(n, 2)
	right := resultChildAt(n, 3)
	if left == nil || errNode == nil || gap == nil || right == nil ||
		errNode.Type(lang) != "ERROR" ||
		gap.Type(lang) != "concatenating_space" ||
		right.Type(lang) != "identifier" ||
		gap.startByte >= gap.endByte ||
		int(gap.endByte) > len(source) ||
		(source[gap.startByte] != '<' && source[gap.startByte] != '>') {
		return nil, false
	}
	opName := string(source[gap.startByte : gap.startByte+1])
	opSym, ok := symbolByName(lang, opName)
	if !ok {
		return nil, false
	}
	binarySym, ok := symbolByName(lang, "binary_exp")
	if !ok {
		return nil, false
	}
	opStart := gap.startByte
	opEnd := opStart + 1
	opStartPoint := gap.startPoint
	opEndPoint := advancePointByBytes(opStartPoint, source[opStart:opEnd])
	op := newLeafNodeInArena(arena, opSym, false, opStart, opEnd, opStartPoint, opEndPoint)
	fields := make([]FieldID, 4)
	if leftField, ok := lang.FieldByName("left"); ok {
		fields[0] = leftField
	}
	if opField, ok := lang.FieldByName("operator"); ok {
		fields[2] = opField
	}
	if rightField, ok := lang.FieldByName("right"); ok {
		fields[3] = rightField
	}
	out := newParentNodeInArena(arena, binarySym, symbolIsNamed(lang, binarySym), []*Node{
		cloneTreeNodesIntoArena(left, arena),
		cloneTreeNodesIntoArena(errNode, arena),
		op,
		cloneTreeNodesIntoArena(right, arena),
	}, cloneFieldIDSliceInArena(arena, fields), 0)
	out.startByte = n.startByte
	out.startPoint = n.startPoint
	out.endByte = n.endByte
	out.endPoint = n.endPoint
	out.setHasError(true)
	return out, true
}

func normalizeAwkRecoveredKeywordConcatOperands(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || len(source) == 0 {
		return
	}
	identSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return
	}
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "string_concat" && resultChildCount(n) >= 4 {
			for i := 1; i < resultChildCount(n); i++ {
				child := resultChildAt(n, i)
				if child == nil || child.isNamed() || resultChildCount(child) != 0 {
					continue
				}
				if child.Type(lang) != "BEGIN" && child.Type(lang) != "END" {
					continue
				}
				prev := resultChildAt(n, i-1)
				if prev == nil || prev.Type(lang) != "concatenating_space" || !awkConcatHasRecoveredErrorBefore(n, i, lang) {
					continue
				}
				child.symbol = identSym
				child.setNamed(true)
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			visit(resultChildAt(n, i))
		}
	}
	visit(root)
}

func awkConcatHasRecoveredErrorBefore(n *Node, end int, lang *Language) bool {
	for i := 0; i < end; i++ {
		child := resultChildAt(n, i)
		if child != nil && child.Type(lang) == "ERROR" {
			return true
		}
	}
	return false
}

func awkMergeRecoveredFuncDefSplit(left, errNode, funcDef *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if left == nil || errNode == nil || funcDef == nil || int(funcDef.endByte) > len(source) {
		return nil, false
	}
	if left.Type(lang) != "rule" || errNode.Type(lang) != "ERROR" || funcDef.Type(lang) != "func_def" {
		return nil, false
	}
	if resultChildCount(left) != 1 || resultChildCount(funcDef) != 5 {
		return nil, false
	}
	if !bytes.Equal(source[errNode.startByte:errNode.endByte], []byte(" '")) {
		return nil, false
	}
	if !awkNoNewlineGap(source, left.endByte, errNode.startByte) || !awkNoNewlineGap(source, errNode.endByte, funcDef.startByte) {
		return nil, false
	}
	leftPattern := resultChildAt(left, 0)
	if leftPattern == nil || leftPattern.Type(lang) != "pattern" || resultChildCount(leftPattern) != 1 {
		return nil, false
	}
	prefixExpr := resultChildAt(leftPattern, 0)
	functionTok := resultChildAt(funcDef, 0)
	name := resultChildAt(funcDef, 1)
	lparen := resultChildAt(funcDef, 2)
	rparen := resultChildAt(funcDef, 3)
	block := resultChildAt(funcDef, 4)
	if prefixExpr == nil || functionTok == nil || name == nil || lparen == nil || rparen == nil || block == nil {
		return nil, false
	}
	if name.Type(lang) != "identifier" || block.Type(lang) != "block" {
		return nil, false
	}

	identifierSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return nil, false
	}
	functionIdent := newLeafNodeInArena(arena, identifierSym, true, functionTok.startByte, functionTok.endByte, functionTok.startPoint, functionTok.endPoint)
	grouping, ok := awkRecoveredEmptyGrouping(lparen, rparen, source, lang, arena)
	if !ok {
		return nil, false
	}

	inner, ok := awkStringConcat(arena, lang, []*Node{
		cloneTreeNodesIntoArena(prefixExpr, arena),
		cloneTreeNodesIntoArena(errNode, arena),
		awkConcatenatingSpaceLeaf(arena, source, errNode.endByte, errNode.endByte, lang),
		functionIdent,
	})
	if !ok {
		return nil, false
	}
	middle, ok := awkStringConcat(arena, lang, []*Node{
		inner,
		awkConcatenatingSpaceLeaf(arena, source, functionTok.endByte, name.startByte, lang),
		cloneTreeNodesIntoArena(name, arena),
	})
	if !ok {
		return nil, false
	}
	outer, ok := awkStringConcat(arena, lang, []*Node{
		middle,
		awkConcatenatingSpaceLeaf(arena, source, name.endByte, lparen.startByte, lang),
		grouping,
	})
	if !ok {
		return nil, false
	}

	patternSym, ok := symbolByName(lang, "pattern")
	if !ok {
		return nil, false
	}
	pattern := newParentNodeInArena(arena, patternSym, symbolIsNamed(lang, patternSym), cloneNodeSliceInArena(arena, []*Node{outer}), nil, 0)
	pattern.startByte = left.startByte
	pattern.startPoint = left.startPoint
	pattern.endByte = rparen.endByte
	pattern.endPoint = rparen.endPoint
	pattern.setHasError(true)

	ruleSym, ok := symbolByName(lang, "rule")
	if !ok {
		return nil, false
	}
	rule := newParentNodeInArena(arena, ruleSym, symbolIsNamed(lang, ruleSym), cloneNodeSliceInArena(arena, []*Node{
		pattern,
		cloneTreeNodesIntoArena(block, arena),
	}), nil, 0)
	rule.startByte = left.startByte
	rule.startPoint = left.startPoint
	rule.endByte = funcDef.endByte
	rule.endPoint = funcDef.endPoint
	rule.setHasError(true)
	return rule, true
}

func awkMergeRecoveredRuleSplit(left, errNode, right *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if left == nil || errNode == nil || right == nil || int(right.endByte) > len(source) {
		return nil, false
	}
	if left.Type(lang) != "rule" || errNode.Type(lang) != "ERROR" || right.Type(lang) != "rule" {
		return nil, false
	}
	if resultChildCount(left) != 1 || resultChildCount(right) == 0 {
		return nil, false
	}
	if !awkCanFoldTopLevelError(errNode, source) {
		return nil, false
	}
	if !awkNoNewlineGap(source, left.endByte, errNode.startByte) || !awkNoNewlineGap(source, errNode.endByte, right.startByte) {
		return nil, false
	}
	leftPattern := resultChildAt(left, 0)
	rightPattern := resultChildAt(right, 0)
	if leftPattern == nil || rightPattern == nil || leftPattern.Type(lang) != "pattern" || rightPattern.Type(lang) != "pattern" {
		return nil, false
	}
	if resultChildCount(leftPattern) != 1 || resultChildCount(rightPattern) != 1 {
		return nil, false
	}
	prefixExpr := resultChildAt(leftPattern, 0)
	rightExpr := resultChildAt(rightPattern, 0)
	if prefixExpr == nil || rightExpr == nil {
		return nil, false
	}
	rewrittenExpr, ok := awkPrependRecoveredExpr(prefixExpr, errNode, rightExpr, source, lang, arena)
	if !ok {
		return nil, false
	}
	rewrittenPattern := cloneNodeInArena(arena, rightPattern)
	replaceNodeChildrenUnfielded(rewrittenPattern, cloneNodeSliceInArena(arena, []*Node{rewrittenExpr}))
	rewrittenPattern.startByte = left.startByte
	rewrittenPattern.startPoint = left.startPoint
	rewrittenPattern.setHasError(true)

	rewrittenRule := cloneNodeInArena(arena, right)
	rightChildren := resultChildSliceForMutation(right)
	newChildren := make([]*Node, 0, len(rightChildren))
	newChildren = append(newChildren, rewrittenPattern)
	if len(rightChildren) > 1 {
		newChildren = append(newChildren, rightChildren[1:]...)
	}
	replaceNodeChildrenUnfielded(rewrittenRule, cloneNodeSliceInArena(arena, newChildren))
	rewrittenRule.startByte = left.startByte
	rewrittenRule.startPoint = left.startPoint
	rewrittenRule.setHasError(true)
	return rewrittenRule, true
}

func awkRecoveredEmptyGrouping(lparen, rparen *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	groupingSym, ok := symbolByName(lang, "grouping")
	if !ok || lparen == nil || rparen == nil || int(rparen.endByte) > len(source) {
		return nil, false
	}
	identifierSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return nil, false
	}
	missing := newLeafNodeInArena(arena, identifierSym, true, lparen.endByte, lparen.endByte, lparen.endPoint, lparen.endPoint)
	missing.setMissing(true)
	missing.setHasError(true)
	grouping := newParentNodeInArena(arena, groupingSym, symbolIsNamed(lang, groupingSym), cloneNodeSliceInArena(arena, []*Node{
		cloneTreeNodesIntoArena(lparen, arena),
		missing,
		cloneTreeNodesIntoArena(rparen, arena),
	}), nil, 0)
	grouping.startByte = lparen.startByte
	grouping.startPoint = lparen.startPoint
	grouping.endByte = rparen.endByte
	grouping.endPoint = rparen.endPoint
	grouping.setHasError(true)
	return grouping, true
}

func awkPrependRecoveredExpr(prefixExpr, errNode, rightExpr *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	if prefixExpr == nil || errNode == nil || rightExpr == nil {
		return nil, false
	}
	rightClone := cloneTreeNodesIntoArena(rightExpr, arena)
	leftmost := awkLeftmostConcatOperand(rightClone, lang)
	if leftmost == nil {
		return nil, false
	}
	parent, childIndex, hasParent := nodeParentLink(leftmost)
	combined, ok := awkCombinedStringConcat(prefixExpr, errNode, leftmost, source, lang, arena)
	if !ok {
		return nil, false
	}
	if !hasParent {
		return combined, true
	}
	if childIndex < 0 || childIndex >= resultChildCount(parent) {
		return nil, false
	}
	children := resultChildSliceForMutation(parent)
	children[childIndex] = combined
	replaceNodeChildrenUnfielded(parent, cloneNodeSliceInArena(arena, children))
	awkSetStringConcatFields(parent, lang)
	for n := parent; n != nil; n = n.parent {
		n.startByte = combined.startByte
		n.startPoint = combined.startPoint
		n.setHasError(true)
		if n == rightClone {
			break
		}
	}
	return rightClone, true
}

func awkCombinedStringConcat(prefixExpr, errNode, firstRight *Node, source []byte, lang *Language, arena *nodeArena) (*Node, bool) {
	children := []*Node{
		cloneTreeNodesIntoArena(prefixExpr, arena),
		cloneTreeNodesIntoArena(errNode, arena),
	}
	if gap := awkConcatenatingSpaceForGap(arena, source, errNode.endByte, firstRight.startByte, lang); gap != nil {
		children = append(children, gap)
	}
	children = append(children, firstRight)
	return awkStringConcat(arena, lang, children)
}

func awkStringConcat(arena *nodeArena, lang *Language, children []*Node) (*Node, bool) {
	concatSym, ok := symbolByName(lang, "string_concat")
	if !ok || len(children) == 0 || children[0] == nil || children[len(children)-1] == nil {
		return nil, false
	}
	for _, child := range children {
		if child == nil {
			return nil, false
		}
	}
	fields := awkStringConcatFields(lang, len(children))
	concat := newParentNodeInArena(arena, concatSym, symbolIsNamed(lang, concatSym), cloneNodeSliceInArena(arena, children), cloneFieldIDSliceInArena(arena, fields), 0)
	concat.startByte = children[0].startByte
	concat.startPoint = children[0].startPoint
	concat.endByte = children[len(children)-1].endByte
	concat.endPoint = children[len(children)-1].endPoint
	concat.setHasError(true)
	return concat, true
}

func awkStringConcatFields(lang *Language, childCount int) []FieldID {
	if lang == nil || childCount <= 0 {
		return nil
	}
	fields := make([]FieldID, childCount)
	if leftField, ok := lang.FieldByName("left"); ok {
		fields[0] = leftField
	}
	if rightField, ok := lang.FieldByName("right"); ok {
		fields[len(fields)-1] = rightField
	}
	return fields
}

func awkSetStringConcatFields(n *Node, lang *Language) {
	if n == nil || lang == nil || n.Type(lang) != "string_concat" || len(n.children) == 0 {
		return
	}
	fieldIDs := cloneFieldIDSliceInArena(n.ownerArena, awkStringConcatFields(lang, len(n.children)))
	n.setFieldMetadata(fieldIDs, defaultFieldSourcesInArena(n.ownerArena, fieldIDs))
}

func awkLeftmostConcatOperand(n *Node, lang *Language) *Node {
	for n != nil && n.Type(lang) == "string_concat" && resultChildCount(n) > 0 {
		n = resultChildAt(n, 0)
	}
	return n
}

func awkConcatenatingSpaceForGap(arena *nodeArena, source []byte, start, end uint32, lang *Language) *Node {
	if start > end || int(end) > len(source) || !awkSpacesOnly(source[start:end]) {
		return nil
	}
	if start == end && !awkNeedsZeroWidthConcatAfterError(source, start) {
		return nil
	}
	return awkConcatenatingSpaceLeaf(arena, source, start, end, lang)
}

func awkConcatenatingSpaceLeaf(arena *nodeArena, source []byte, start, end uint32, lang *Language) *Node {
	if start > end || int(end) > len(source) {
		return nil
	}
	sym, ok := symbolByName(lang, "concatenating_space")
	if !ok {
		return nil
	}
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	return newLeafNodeInArena(arena, sym, true, start, end, startPoint, endPoint)
}

func awkCanFoldTopLevelError(n *Node, source []byte) bool {
	if n == nil || n.startByte > n.endByte || int(n.endByte) > len(source) {
		return false
	}
	text := source[n.startByte:n.endByte]
	switch {
	case bytes.Equal(text, []byte(":")),
		bytes.Equal(text, []byte("=")),
		bytes.Equal(text, []byte("&")),
		bytes.Equal(text, []byte("\\&")),
		bytes.Equal(text, []byte("\\&\\&")),
		bytes.Equal(text, []byte(" '")):
		return true
	default:
		return false
	}
}

func awkNoNewlineGap(source []byte, start, end uint32) bool {
	if start > end || int(end) > len(source) {
		return false
	}
	for _, b := range source[start:end] {
		if b == '\n' || b == '\r' {
			return false
		}
	}
	return true
}

func awkSpacesOnly(bs []byte) bool {
	for _, b := range bs {
		if b != ' ' && b != '\t' {
			return false
		}
	}
	return true
}

func awkNeedsZeroWidthConcatAfterError(source []byte, pos uint32) bool {
	return int(pos) < len(source) && source[pos] != '\n' && source[pos] != '\r'
}

func normalizeAwkBareBackslashProgramSpan(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "awk" || root.Type(lang) != "program" {
		return
	}
	if resultChildCount(root) != 0 || len(source) != 1 || source[0] != '\\' {
		return
	}
	end := uint32(len(source))
	point := advancePointByBytes(Point{}, source)
	root.startByte = end
	root.endByte = end
	root.startPoint = point
	root.endPoint = point
}
