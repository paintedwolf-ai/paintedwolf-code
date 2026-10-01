package gotreesitter

import "bytes"

func buildPowerShellLogicalExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	logicalSym, logicalNamed, ok := symbolMeta(lang, "logical_expression")
	if !ok {
		return nil
	}
	bitwise := buildPowerShellBitwiseExpression(arena, source, lang, start, end)
	if bitwise == nil {
		return nil
	}
	children := []*Node{bitwise}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = bitwise
		children = buf
	}
	return newParentNodeInArena(arena, logicalSym, logicalNamed, children, nil, 0)
}

func buildPowerShellBitwiseExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	bitwiseSym, bitwiseNamed, ok := symbolMeta(lang, "bitwise_expression")
	if !ok {
		return nil
	}
	comparison := buildPowerShellComparisonExpression(arena, source, lang, start, end)
	if comparison == nil {
		return nil
	}
	children := []*Node{comparison}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = comparison
		children = buf
	}
	return newParentNodeInArena(arena, bitwiseSym, bitwiseNamed, children, nil, 0)
}

func buildPowerShellComparisonExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	comparisonSym, comparisonNamed, ok := symbolMeta(lang, "comparison_expression")
	if !ok {
		return nil
	}
	additive := buildPowerShellAdditiveExpression(arena, source, lang, start, end)
	if additive == nil {
		return nil
	}
	children := []*Node{additive}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = additive
		children = buf
	}
	return newParentNodeInArena(arena, comparisonSym, comparisonNamed, children, nil, 0)
}

func buildPowerShellAdditiveExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	additiveSym, additiveNamed, ok := symbolMeta(lang, "additive_expression")
	if !ok {
		return nil
	}
	start, end = powerShellTrimInlineSpace(source, start, end)
	if start >= end {
		return nil
	}
	if plus := powerShellFindTopLevelPlus(source, start, end); plus >= 0 {
		left := buildPowerShellAdditiveExpression(arena, source, lang, start, plus)
		right := buildPowerShellMultiplicativeExpression(arena, source, lang, plus+1, end)
		plusSym, plusNamed, ok := symbolMeta(lang, "+")
		if !ok || left == nil || right == nil {
			return nil
		}
		children := []*Node{
			left,
			newLeafNodeInArena(arena, plusSym, plusNamed, uint32(plus), uint32(plus+1), advancePointByBytes(Point{}, source[:plus]), advancePointByBytes(advancePointByBytes(Point{}, source[:plus]), source[plus:plus+1])),
			right,
		}
		if arena != nil {
			buf := arena.allocNodeSlice(len(children))
			copy(buf, children)
			children = buf
		}
		return newParentNodeInArena(arena, additiveSym, additiveNamed, children, nil, 0)
	}
	multiplicative := buildPowerShellMultiplicativeExpression(arena, source, lang, start, end)
	if multiplicative == nil {
		return nil
	}
	children := []*Node{multiplicative}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = multiplicative
		children = buf
	}
	return newParentNodeInArena(arena, additiveSym, additiveNamed, children, nil, 0)
}

func buildPowerShellMultiplicativeExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	multiplicativeSym, multiplicativeNamed, ok := symbolMeta(lang, "multiplicative_expression")
	if !ok {
		return nil
	}
	format := buildPowerShellFormatExpression(arena, source, lang, start, end)
	if format == nil {
		return nil
	}
	children := []*Node{format}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = format
		children = buf
	}
	return newParentNodeInArena(arena, multiplicativeSym, multiplicativeNamed, children, nil, 0)
}

func buildPowerShellFormatExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	formatSym, formatNamed, ok := symbolMeta(lang, "format_expression")
	if !ok {
		return nil
	}
	rng := buildPowerShellRangeExpression(arena, source, lang, start, end)
	if rng == nil {
		return nil
	}
	children := []*Node{rng}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = rng
		children = buf
	}
	return newParentNodeInArena(arena, formatSym, formatNamed, children, nil, 0)
}

func buildPowerShellRangeExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	rangeSym, rangeNamed, ok := symbolMeta(lang, "range_expression")
	if !ok {
		return nil
	}
	array := buildPowerShellArrayLiteralExpression(arena, source, lang, start, end)
	if array == nil {
		return nil
	}
	children := []*Node{array}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = array
		children = buf
	}
	return newParentNodeInArena(arena, rangeSym, rangeNamed, children, nil, 0)
}

func buildPowerShellArrayLiteralExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	arraySym, arrayNamed, ok := symbolMeta(lang, "array_literal_expression")
	if !ok {
		return nil
	}
	unary := buildPowerShellUnaryExpression(arena, source, lang, start, end)
	if unary == nil {
		return nil
	}
	children := []*Node{unary}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = unary
		children = buf
	}
	return newParentNodeInArena(arena, arraySym, arrayNamed, children, nil, 0)
}

func buildPowerShellUnaryExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	unarySym, unaryNamed, ok := symbolMeta(lang, "unary_expression")
	if !ok {
		return nil
	}
	core := buildPowerShellExpressionCore(arena, source, lang, start, end)
	if core == nil {
		return nil
	}
	children := []*Node{core}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = core
		children = buf
	}
	return newParentNodeInArena(arena, unarySym, unaryNamed, children, nil, 0)
}

func buildPowerShellExpressionCore(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	start, end = powerShellTrimInlineSpace(source, start, end)
	if start >= end {
		return nil
	}
	switch source[start] {
	case '!':
		exprUnarySym, exprUnaryNamed, ok := symbolMeta(lang, "expression_with_unary_operator")
		if !ok {
			return nil
		}
		bangSym, bangNamed, ok := symbolMeta(lang, "!")
		if !ok {
			return nil
		}
		innerStart := powerShellSkipInlineSpace(source, start+1, end)
		innerCore := buildPowerShellExpressionCore(arena, source, lang, innerStart, end)
		if innerCore == nil {
			return nil
		}
		innerUnary := wrapPowerShellExpression(arena, lang, innerCore, "unary_expression")
		if innerUnary == nil {
			return nil
		}
		children := []*Node{
			newLeafNodeInArena(arena, bangSym, bangNamed, uint32(start), uint32(start+1), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:start+1])),
			innerUnary,
		}
		if arena != nil {
			buf := arena.allocNodeSlice(len(children))
			copy(buf, children)
			children = buf
		}
		return newParentNodeInArena(arena, exprUnarySym, exprUnaryNamed, children, nil, 0)
	case '(':
		return buildPowerShellParenthesizedExpression(arena, source, lang, start, end)
	case '"':
		stringLiteralSym, stringLiteralNamed, ok := symbolMeta(lang, "string_literal")
		if !ok {
			return nil
		}
		expandable := buildPowerShellExpandableStringLiteral(arena, source, lang, start, end)
		if expandable == nil {
			return nil
		}
		children := []*Node{expandable}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = expandable
			children = buf
		}
		return newParentNodeInArena(arena, stringLiteralSym, stringLiteralNamed, children, nil, 0)
	case '$':
		variableSym, variableNamed, ok := symbolMeta(lang, "variable")
		if !ok {
			return nil
		}
		if bytes.ContainsAny(source[start:end], " \t") {
			genericSym, genericNamed, ok := symbolMeta(lang, "generic_token")
			if !ok {
				return nil
			}
			return newLeafNodeInArena(arena, genericSym, genericNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
		}
		return newLeafNodeInArena(arena, variableSym, variableNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	case '[':
		if bytes.Contains(source[start:end], []byte("::")) {
			if end > start && source[end-1] == ')' {
				if inv := buildPowerShellInvokationExpression(arena, source, lang, start, end); inv != nil {
					return inv
				}
			}
			if member := buildPowerShellMemberAccessExpression(arena, source, lang, start, end); member != nil {
				return member
			}
		}
		genericSym, genericNamed, ok := symbolMeta(lang, "generic_token")
		if !ok {
			return nil
		}
		return newLeafNodeInArena(arena, genericSym, genericNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	default:
		genericSym, genericNamed, ok := symbolMeta(lang, "generic_token")
		if !ok {
			return nil
		}
		return newLeafNodeInArena(arena, genericSym, genericNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	}
}

func buildPowerShellParenthesizedExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	parenthesizedSym, parenthesizedNamed, ok := symbolMeta(lang, "parenthesized_expression")
	if !ok {
		return nil
	}
	openParenSym, _, ok := symbolMeta(lang, "(")
	if !ok {
		return nil
	}
	closeParenSym, _, ok := symbolMeta(lang, ")")
	if !ok {
		return nil
	}
	if end-start < 2 || source[start] != '(' || source[end-1] != ')' {
		return nil
	}
	innerStart, innerEnd := powerShellTrimInlineSpace(source, start+1, end-1)
	innerIsCommand := innerStart < innerEnd && powerShellLooksLikeCommandText(source, innerStart, innerEnd)
	var inner *Node
	if innerStart < innerEnd {
		if innerIsCommand {
			inner = buildPowerShellRecoveredPipeline(arena, source, lang, innerStart, innerEnd)
		}
		if inner == nil {
			inner = buildPowerShellRecoveredConditionPipeline(arena, source, lang, innerStart, innerEnd)
		}
	}
	children := make([]*Node, 0, 3)
	children = append(children, newLeafNodeInArena(arena, openParenSym, false, uint32(start), uint32(start+1), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:start+1])))
	if inner != nil {
		children = append(children, inner)
	}
	children = append(children, newLeafNodeInArena(arena, closeParenSym, false, uint32(end-1), uint32(end), advancePointByBytes(Point{}, source[:end-1]), advancePointByBytes(advancePointByBytes(Point{}, source[:end-1]), source[end-1:end])))
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	node := newParentNodeInArena(arena, parenthesizedSym, parenthesizedNamed, children, nil, 0)
	if !innerIsCommand {
		node.setHasError(true)
	}
	return node
}

func buildPowerShellInvokationExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	invocationSym, invocationNamed, ok := symbolMeta(lang, "invokation_expression")
	if !ok {
		return nil
	}
	typeClose := findMatchingDelimitedByte(source, start, end, '[', ']')
	if typeClose < 0 {
		return nil
	}
	memberStart := typeClose + 1
	if memberStart+2 >= end || source[memberStart] != ':' || source[memberStart+1] != ':' {
		return nil
	}
	nameStart := memberStart + 2
	openParen := findMatchingPowerShellToken(source, nameStart, end, '(')
	if openParen < 0 {
		return nil
	}
	closeParen := findMatchingDelimitedByte(source, openParen, end, '(', ')')
	if closeParen != end-1 {
		return nil
	}
	typeLiteral := buildPowerShellTypeLiteral(arena, source, lang, start, typeClose+1)
	memberName := buildPowerShellMemberName(arena, source, lang, nameStart, openParen)
	argumentList := buildPowerShellArgumentList(arena, source, lang, openParen, closeParen+1)
	colonColonSym, colonColonNamed, ok := symbolMeta(lang, "::")
	if !ok || typeLiteral == nil || memberName == nil || argumentList == nil {
		return nil
	}
	children := []*Node{
		typeLiteral,
		newLeafNodeInArena(arena, colonColonSym, colonColonNamed, uint32(memberStart), uint32(memberStart+2), advancePointByBytes(Point{}, source[:memberStart]), advancePointByBytes(advancePointByBytes(Point{}, source[:memberStart]), source[memberStart:memberStart+2])),
		memberName,
		argumentList,
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	return newParentNodeInArena(arena, invocationSym, invocationNamed, children, nil, 0)
}

func buildPowerShellMemberAccessExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	memberAccessSym, memberAccessNamed, ok := symbolMeta(lang, "member_access")
	if !ok {
		return nil
	}
	typeClose := findMatchingDelimitedByte(source, start, end, '[', ']')
	if typeClose < 0 {
		return nil
	}
	memberStart := typeClose + 1
	if memberStart+2 > end || source[memberStart] != ':' || source[memberStart+1] != ':' {
		return nil
	}
	nameStart := memberStart + 2
	typeLiteral := buildPowerShellTypeLiteral(arena, source, lang, start, typeClose+1)
	memberName := buildPowerShellMemberName(arena, source, lang, nameStart, end)
	colonColonSym, colonColonNamed, ok := symbolMeta(lang, "::")
	if !ok || typeLiteral == nil || memberName == nil {
		return nil
	}
	children := []*Node{
		typeLiteral,
		newLeafNodeInArena(arena, colonColonSym, colonColonNamed, uint32(memberStart), uint32(memberStart+2), advancePointByBytes(Point{}, source[:memberStart]), advancePointByBytes(advancePointByBytes(Point{}, source[:memberStart]), source[memberStart:memberStart+2])),
		memberName,
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	return newParentNodeInArena(arena, memberAccessSym, memberAccessNamed, children, nil, 0)
}

func buildPowerShellTypeLiteral(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	typeLiteralSym, typeLiteralNamed, ok := symbolMeta(lang, "type_literal")
	if !ok {
		return nil
	}
	openBracketSym, openBracketNamed, ok := symbolMeta(lang, "[")
	if !ok {
		return nil
	}
	closeBracketSym, closeBracketNamed, ok := symbolMeta(lang, "]")
	if !ok {
		return nil
	}
	typeSpec := buildPowerShellTypeSpec(arena, source, lang, start+1, end-1)
	if typeSpec == nil {
		return nil
	}
	children := make([]*Node, 0, 4)
	children = append(children, newLeafNodeInArena(arena, openBracketSym, openBracketNamed, uint32(start), uint32(start+1), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:start+1])))
	children = append(children, typeSpec)
	if plus := powerShellFindTopLevelPlus(source, start+1, end-1); plus >= 0 {
		plusSym, plusNamed, ok := symbolMeta(lang, "+")
		if !ok {
			return nil
		}
		errChildren := []*Node{
			newLeafNodeInArena(arena, plusSym, plusNamed, uint32(plus), uint32(plus+1), advancePointByBytes(Point{}, source[:plus]), advancePointByBytes(advancePointByBytes(Point{}, source[:plus]), source[plus:plus+1])),
		}
		if simpleName := buildPowerShellSimpleName(arena, source, lang, plus+1, end-1); simpleName != nil {
			errChildren = append(errChildren, simpleName)
		}
		if arena != nil {
			buf := arena.allocNodeSlice(len(errChildren))
			copy(buf, errChildren)
			errChildren = buf
		}
		errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
		errNode.setHasError(true)
		errNode.setExtra(true)
		children = append(children, errNode)
	}
	children = append(children, newLeafNodeInArena(arena, closeBracketSym, closeBracketNamed, uint32(end-1), uint32(end), advancePointByBytes(Point{}, source[:end-1]), advancePointByBytes(advancePointByBytes(Point{}, source[:end-1]), source[end-1:end])))
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	node := newParentNodeInArena(arena, typeLiteralSym, typeLiteralNamed, children, nil, 0)
	if len(children) == 4 {
		node.setHasError(true)
	}
	return node
}

func buildPowerShellTypeSpec(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	typeSpecSym, typeSpecNamed, ok := symbolMeta(lang, "type_spec")
	if !ok {
		return nil
	}
	if plus := powerShellFindTopLevelPlus(source, start, end); plus >= 0 {
		end = plus
	}
	typeName := buildPowerShellTypeName(arena, source, lang, start, end)
	if typeName == nil {
		return nil
	}
	children := []*Node{typeName}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = typeName
		children = buf
	}
	return newParentNodeInArena(arena, typeSpecSym, typeSpecNamed, children, nil, 0)
}

func buildPowerShellTypeName(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	typeNameSym, typeNameNamed, ok := symbolMeta(lang, "type_name")
	if !ok {
		return nil
	}
	typeIdentifierSym, typeIdentifierNamed, ok := symbolMeta(lang, "type_identifier")
	if !ok {
		return nil
	}
	if dot := bytes.LastIndexByte(source[start:end], '.'); dot >= 0 {
		dot += start
		left := buildPowerShellTypeName(arena, source, lang, start, dot)
		right := newLeafNodeInArena(arena, typeIdentifierSym, typeIdentifierNamed, uint32(dot+1), uint32(end), advancePointByBytes(Point{}, source[:dot+1]), advancePointByBytes(advancePointByBytes(Point{}, source[:dot+1]), source[dot+1:end]))
		dotSym, dotNamed, ok := symbolMeta(lang, ".")
		if !ok || left == nil {
			return nil
		}
		children := []*Node{
			left,
			newLeafNodeInArena(arena, dotSym, dotNamed, uint32(dot), uint32(dot+1), advancePointByBytes(Point{}, source[:dot]), advancePointByBytes(advancePointByBytes(Point{}, source[:dot]), source[dot:dot+1])),
			right,
		}
		if arena != nil {
			buf := arena.allocNodeSlice(len(children))
			copy(buf, children)
			children = buf
		}
		return newParentNodeInArena(arena, typeNameSym, typeNameNamed, children, nil, 0)
	}
	leaf := newLeafNodeInArena(arena, typeIdentifierSym, typeIdentifierNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	children := []*Node{leaf}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = leaf
		children = buf
	}
	return newParentNodeInArena(arena, typeNameSym, typeNameNamed, children, nil, 0)
}

func buildPowerShellMemberName(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	memberNameSym, memberNameNamed, ok := symbolMeta(lang, "member_name")
	if !ok {
		return nil
	}
	simpleName := buildPowerShellSimpleName(arena, source, lang, start, end)
	if simpleName == nil {
		return nil
	}
	children := []*Node{simpleName}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = simpleName
		children = buf
	}
	return newParentNodeInArena(arena, memberNameSym, memberNameNamed, children, nil, 0)
}

func buildPowerShellSimpleName(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	simpleNameSym, simpleNameNamed, ok := symbolMeta(lang, "simple_name")
	if !ok {
		return nil
	}
	leaf := newLeafNodeInArena(arena, simpleNameSym, simpleNameNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	return leaf
}

func buildPowerShellArgumentList(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	argumentListSym, argumentListNamed, ok := symbolMeta(lang, "argument_list")
	if !ok {
		return nil
	}
	argumentExprListSym, argumentExprListNamed, ok := symbolMeta(lang, "argument_expression_list")
	if !ok {
		return nil
	}
	openParenSym, openParenNamed, ok := symbolMeta(lang, "(")
	if !ok {
		return nil
	}
	closeParenSym, closeParenNamed, ok := symbolMeta(lang, ")")
	if !ok {
		return nil
	}
	argStart, argEnd := powerShellTrimInlineSpace(source, start+1, end-1)
	argument := buildPowerShellArgumentExpression(arena, source, lang, argStart, argEnd)
	if argument == nil {
		return nil
	}
	listChildren := []*Node{argument}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = argument
		listChildren = buf
	}
	argumentListChildren := []*Node{
		newLeafNodeInArena(arena, openParenSym, openParenNamed, uint32(start), uint32(start+1), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:start+1])),
		newParentNodeInArena(arena, argumentExprListSym, argumentExprListNamed, listChildren, nil, 0),
		newLeafNodeInArena(arena, closeParenSym, closeParenNamed, uint32(end-1), uint32(end), advancePointByBytes(Point{}, source[:end-1]), advancePointByBytes(advancePointByBytes(Point{}, source[:end-1]), source[end-1:end])),
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(argumentListChildren))
		copy(buf, argumentListChildren)
		argumentListChildren = buf
	}
	argList := newParentNodeInArena(arena, argumentListSym, argumentListNamed, argumentListChildren, nil, 0)
	for fieldIdx, fieldName := range lang.FieldNames {
		if fieldName != "argument_expression_list" {
			continue
		}
		ensureNodeFieldStorage(argList, len(argList.children))
		argList.fieldIDs()[1] = FieldID(fieldIdx)
		argList.fieldSources()[1] = fieldSourceDirect
		break
	}
	return argList
}

func buildPowerShellArgumentExpression(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	argumentExprSym, argumentExprNamed, ok := symbolMeta(lang, "argument_expression")
	if !ok {
		return nil
	}
	logicalArgSym, logicalArgNamed, ok := symbolMeta(lang, "logical_argument_expression")
	if !ok {
		return nil
	}
	bitwiseArgSym, bitwiseArgNamed, ok := symbolMeta(lang, "bitwise_argument_expression")
	if !ok {
		return nil
	}
	comparisonArgSym, comparisonArgNamed, ok := symbolMeta(lang, "comparison_argument_expression")
	if !ok {
		return nil
	}
	additiveArgSym, additiveArgNamed, ok := symbolMeta(lang, "additive_argument_expression")
	if !ok {
		return nil
	}
	multiplicativeArgSym, multiplicativeArgNamed, ok := symbolMeta(lang, "multiplicative_argument_expression")
	if !ok {
		return nil
	}
	formatArgSym, formatArgNamed, ok := symbolMeta(lang, "format_argument_expression")
	if !ok {
		return nil
	}
	rangeArgSym, rangeArgNamed, ok := symbolMeta(lang, "range_argument_expression")
	if !ok {
		return nil
	}
	core := buildPowerShellExpressionCore(arena, source, lang, start, end)
	if core == nil {
		return nil
	}
	unary := wrapPowerShellExpression(arena, lang, core, "unary_expression")
	rangeArg := newParentNodeInArena(arena, rangeArgSym, rangeArgNamed, []*Node{rangeToArenaChild(arena, unary)}, nil, 0)
	formatArg := newParentNodeInArena(arena, formatArgSym, formatArgNamed, []*Node{rangeToArenaChild(arena, rangeArg)}, nil, 0)
	multiplicativeArg := newParentNodeInArena(arena, multiplicativeArgSym, multiplicativeArgNamed, []*Node{rangeToArenaChild(arena, formatArg)}, nil, 0)
	additiveArg := newParentNodeInArena(arena, additiveArgSym, additiveArgNamed, []*Node{rangeToArenaChild(arena, multiplicativeArg)}, nil, 0)
	comparisonArg := newParentNodeInArena(arena, comparisonArgSym, comparisonArgNamed, []*Node{rangeToArenaChild(arena, additiveArg)}, nil, 0)
	bitwiseArg := newParentNodeInArena(arena, bitwiseArgSym, bitwiseArgNamed, []*Node{rangeToArenaChild(arena, comparisonArg)}, nil, 0)
	logicalArg := newParentNodeInArena(arena, logicalArgSym, logicalArgNamed, []*Node{rangeToArenaChild(arena, bitwiseArg)}, nil, 0)
	children := []*Node{logicalArg}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = logicalArg
		children = buf
	}
	return newParentNodeInArena(arena, argumentExprSym, argumentExprNamed, children, nil, 0)
}

func rangeToArenaChild(arena *nodeArena, child *Node) *Node {
	return child
}

func findMatchingPowerShellToken(source []byte, start, end int, target byte) int {
	for i := start; i < end; i++ {
		if source[i] == target {
			return i
		}
	}
	return -1
}

func wrapPowerShellExpression(arena *nodeArena, lang *Language, core *Node, types ...string) *Node {
	if core == nil || lang == nil {
		return nil
	}
	node := core
	for _, typeName := range types {
		sym, named, ok := symbolMeta(lang, typeName)
		if !ok {
			return nil
		}
		children := []*Node{node}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = node
			children = buf
		}
		node = newParentNodeInArena(arena, sym, named, children, nil, 0)
	}
	return node
}
