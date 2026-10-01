package gotreesitter

func normalizeDartCompatibility(root *Node, source []byte, lang *Language) {
	normalizeDartConstructorSignatureKinds(root, source, lang)
	normalizeDartSingleTypeArgumentFreeCalls(root, lang)
	normalizeDartComplexTypeArgumentRelationalFreeCalls(root, lang)
	normalizeDartNestedComplexTypeArgumentRelationalCalls(root, lang)
	normalizeDartComplexTypeArgumentFreeCalls(root, source, lang)
}

func normalizeDartSingleTypeArgumentFreeCalls(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dart" {
		return
	}
	relExprSym, ok := lang.SymbolByName("relational_expression")
	if !ok {
		return
	}
	relOpSym, ok := lang.SymbolByName("relational_operator")
	if !ok {
		return
	}
	parenSym, ok := lang.SymbolByName("parenthesized_expression")
	if !ok {
		return
	}
	relExprNamed := symbolIsNamed(lang, relExprSym)
	relOpNamed := symbolIsNamed(lang, relOpSym)
	parenNamed := symbolIsNamed(lang, parenSym)

	walkResultTree(root, func(n *Node) {
		for i := 0; i+1 < len(n.children); i++ {
			if rewriteDartSingleTypeArgumentFreeCall(n, i, lang, relExprSym, relExprNamed, relOpSym, relOpNamed, parenSym, parenNamed) {
				break
			}
		}
	})
}

func normalizeDartComplexTypeArgumentFreeCalls(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dart" || len(source) == 0 {
		return
	}
	ctx, ok := newDartComplexTypeArgumentFreeCallContext(lang)
	if !ok {
		return
	}
	walkResultTree(root, func(parent *Node) {
		for i := 0; i < resultChildCount(parent); i++ {
			child := resultChildAt(parent, i)
			callee, selector, ok := rewriteDartComplexTypeArgumentFreeCallParts(child, source, lang, ctx)
			if !ok {
				continue
			}
			replaceChildRangeWithNodes(parent, i, i+1, []*Node{callee, selector})
			i++
		}
	})
}

type dartComplexTypeArgumentFreeCallContext struct {
	selectorSym                         Symbol
	selectorNamed                       bool
	unconditionalAssignableSelectorSym  Symbol
	unconditionalAssignableSelectorName bool
	argumentPartSym                     Symbol
	argumentPartNamed                   bool
	argumentsSym                        Symbol
	argumentsNamed                      bool
	argumentSym                         Symbol
	argumentNamed                       bool
	relationalExpressionSym             Symbol
	relationalExpressionNamed           bool
	relationalOperatorSym               Symbol
	relationalOperatorNamed             bool
	parenthesizedExpressionSym          Symbol
	parenthesizedExpressionNamed        bool
	identifierSym                       Symbol
	identifierNamed                     bool
	typeArgumentsSym                    Symbol
	typeArgumentsName                   bool
	typeIdentifierSym                   Symbol
	typeIdentifier                      bool
}

func newDartComplexTypeArgumentFreeCallContext(lang *Language) (dartComplexTypeArgumentFreeCallContext, bool) {
	var ctx dartComplexTypeArgumentFreeCallContext
	var ok bool
	if ctx.selectorSym, ok = lang.SymbolByName("selector"); !ok {
		return ctx, false
	}
	if ctx.unconditionalAssignableSelectorSym, ok = lang.SymbolByName("unconditional_assignable_selector"); !ok {
		return ctx, false
	}
	if ctx.argumentPartSym, ok = lang.SymbolByName("argument_part"); !ok {
		return ctx, false
	}
	if ctx.argumentsSym, ok = lang.SymbolByName("arguments"); !ok {
		return ctx, false
	}
	if ctx.argumentSym, ok = lang.SymbolByName("argument"); !ok {
		return ctx, false
	}
	if ctx.relationalExpressionSym, ok = lang.SymbolByName("relational_expression"); !ok {
		return ctx, false
	}
	if ctx.relationalOperatorSym, ok = lang.SymbolByName("relational_operator"); !ok {
		return ctx, false
	}
	if ctx.parenthesizedExpressionSym, ok = lang.SymbolByName("parenthesized_expression"); !ok {
		return ctx, false
	}
	if ctx.identifierSym, ok = lang.SymbolByName("identifier"); !ok {
		return ctx, false
	}
	if ctx.typeArgumentsSym, ok = lang.SymbolByName("type_arguments"); !ok {
		return ctx, false
	}
	if ctx.typeIdentifierSym, ok = lang.SymbolByName("type_identifier"); !ok {
		return ctx, false
	}
	ctx.selectorNamed = symbolIsNamed(lang, ctx.selectorSym)
	ctx.unconditionalAssignableSelectorName = symbolIsNamed(lang, ctx.unconditionalAssignableSelectorSym)
	ctx.argumentPartNamed = symbolIsNamed(lang, ctx.argumentPartSym)
	ctx.argumentsNamed = symbolIsNamed(lang, ctx.argumentsSym)
	ctx.argumentNamed = symbolIsNamed(lang, ctx.argumentSym)
	ctx.relationalExpressionNamed = symbolIsNamed(lang, ctx.relationalExpressionSym)
	ctx.relationalOperatorNamed = symbolIsNamed(lang, ctx.relationalOperatorSym)
	ctx.parenthesizedExpressionNamed = symbolIsNamed(lang, ctx.parenthesizedExpressionSym)
	ctx.identifierNamed = symbolIsNamed(lang, ctx.identifierSym)
	ctx.typeArgumentsName = symbolIsNamed(lang, ctx.typeArgumentsSym)
	ctx.typeIdentifier = symbolIsNamed(lang, ctx.typeIdentifierSym)
	return ctx, true
}

func normalizeDartComplexTypeArgumentRelationalFreeCalls(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dart" {
		return
	}
	ctx, ok := newDartComplexTypeArgumentFreeCallContext(lang)
	if !ok {
		return
	}
	walkResultTree(root, func(parent *Node) {
		for i := 0; i+1 < resultChildCount(parent); i++ {
			if rewriteDartComplexTypeArgumentRelationalFreeCall(parent, i, lang, ctx) {
				break
			}
		}
	})
}

func rewriteDartComplexTypeArgumentRelationalFreeCall(parent *Node, idx int, lang *Language, ctx dartComplexTypeArgumentFreeCallContext) bool {
	if parent == nil || idx < 0 || idx+1 >= resultChildCount(parent) || lang == nil {
		return false
	}
	callee := resultChildAt(parent, idx)
	selector := resultChildAt(parent, idx+1)
	if callee == nil || selector == nil || callee.Type(lang) != "identifier" || selector.Type(lang) != "selector" || resultChildCount(selector) != 1 {
		return false
	}
	argPart := resultChildAt(selector, 0)
	if argPart == nil || argPart.Type(lang) != "argument_part" || resultChildCount(argPart) != 2 {
		return false
	}
	typeArgs := resultChildAt(argPart, 0)
	args := resultChildAt(argPart, 1)
	if typeArgs == nil || args == nil || typeArgs.Type(lang) != "type_arguments" || args.Type(lang) != "arguments" {
		return false
	}
	if !dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(typeArgs, lang) {
		return false
	}
	lt, typeArgParts, gt, ok := dartRelationalPartsFromDartTypeArguments(typeArgs, lang, ctx, parent.ownerArena)
	if !ok {
		return false
	}
	arena := parent.ownerArena
	leftChildren := make([]*Node, 0, len(typeArgParts)+2)
	leftChildren = append(leftChildren, cloneTreeNodesIntoArena(callee, arena))
	leftChildren = append(leftChildren, newParentNodeInArena(arena, ctx.relationalOperatorSym, ctx.relationalOperatorNamed, []*Node{lt}, nil, 0))
	leftChildren = append(leftChildren, typeArgParts...)
	left := newParentNodeInArena(arena, ctx.relationalExpressionSym, ctx.relationalExpressionNamed, leftChildren, nil, 0)
	greaterOp := newParentNodeInArena(arena, ctx.relationalOperatorSym, ctx.relationalOperatorNamed, []*Node{gt}, nil, 0)
	paren := newParentNodeInArena(arena, ctx.parenthesizedExpressionSym, ctx.parenthesizedExpressionNamed, dartParenthesizedExpressionChildren(args, lang), nil, args.productionID)
	outer := newParentNodeInArena(arena, ctx.relationalExpressionSym, ctx.relationalExpressionNamed, []*Node{left, greaterOp, paren}, nil, 0)
	replaceChildRangeWithSingleNode(parent, idx, idx+2, outer)
	return true
}

func normalizeDartNestedComplexTypeArgumentRelationalCalls(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dart" {
		return
	}
	walkResultTree(root, func(n *Node) {
		rewriteDartNestedComplexTypeArgumentRelationalCall(n, lang)
	})
}

func rewriteDartNestedComplexTypeArgumentRelationalCall(n *Node, lang *Language) bool {
	if n == nil || lang == nil || n.Type(lang) != "relational_expression" || resultChildCount(n) != 3 {
		return false
	}
	callee := resultChildAt(n, 0)
	lessOp := resultChildAt(n, 1)
	right := resultChildAt(n, 2)
	if callee == nil || callee.Type(lang) != "identifier" || !dartRelationalOperatorWrapsToken(lessOp, lang, "<") {
		return false
	}
	if right == nil || right.Type(lang) != "relational_expression" || resultChildCount(right) < 5 {
		return false
	}
	rightCount := resultChildCount(right)
	greaterOp := resultChildAt(right, rightCount-2)
	paren := resultChildAt(right, rightCount-1)
	if !dartRelationalOperatorWrapsToken(greaterOp, lang, ">") || paren == nil || paren.Type(lang) != "parenthesized_expression" {
		return false
	}
	if !dartRelationalExpressionPrefixHasNonGenericFunctionTypeArguments(right, lang, rightCount-2) {
		return false
	}
	arena := n.ownerArena
	leftChildren := make([]*Node, 0, rightCount)
	leftChildren = append(leftChildren, cloneTreeNodesIntoArena(callee, arena))
	leftChildren = append(leftChildren, cloneTreeNodesIntoArena(lessOp, arena))
	for i := 0; i < rightCount-2; i++ {
		leftChildren = append(leftChildren, cloneTreeNodesIntoArena(resultChildAt(right, i), arena))
	}
	left := newParentNodeInArena(arena, n.symbol, n.IsNamed(), leftChildren, nil, right.productionID)
	n.children = cloneNodeSliceInArena(arena, []*Node{
		left,
		cloneTreeNodesIntoArena(greaterOp, arena),
		cloneTreeNodesIntoArena(paren, arena),
	})
	n.clearFieldMetadata()
	if n.ownerArena != nil {
		n.ownerArena.clearFinalChildRefs(n)
	}
	populateParentNode(n, n.children)
	return true
}

func dartRelationalExpressionPrefixHasNonGenericFunctionTypeArguments(rel *Node, lang *Language, end int) bool {
	if rel == nil || lang == nil || end <= 0 || end > resultChildCount(rel) {
		return false
	}
	for i := 0; i < end; i++ {
		child := resultChildAt(rel, i)
		if child == nil {
			continue
		}
		if child.Type(lang) == "type_arguments" && dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(child, lang) {
			return true
		}
		if child.Type(lang) == "selector" && resultChildCount(child) == 1 {
			inner := resultChildAt(child, 0)
			if inner != nil && inner.Type(lang) == "type_arguments" && dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(inner, lang) {
				return true
			}
		}
	}
	return false
}

func rewriteDartComplexTypeArgumentFreeCallParts(rel *Node, source []byte, lang *Language, ctx dartComplexTypeArgumentFreeCallContext) (*Node, *Node, bool) {
	if rel == nil || lang == nil || rel.Type(lang) != "relational_expression" || resultChildCount(rel) != 3 {
		return nil, nil, false
	}
	left := resultChildAt(rel, 0)
	greaterOp := resultChildAt(rel, 1)
	paren := resultChildAt(rel, 2)
	if left == nil || left.Type(lang) != "relational_expression" || resultChildCount(left) < 4 {
		return nil, nil, false
	}
	if !dartRelationalOperatorWrapsToken(greaterOp, lang, ">") {
		return nil, nil, false
	}
	callee := resultChildAt(left, 0)
	lessOp := resultChildAt(left, 1)
	if callee == nil || callee.Type(lang) != "identifier" || !dartRelationalOperatorWrapsToken(lessOp, lang, "<") {
		return nil, nil, false
	}
	arena := rel.ownerArena
	typeArgChildren, ok := dartComplexTypeArgumentChildren(left, lessOp, greaterOp, source, lang, ctx, arena)
	if !ok {
		return nil, nil, false
	}
	arguments, ok := dartArgumentsFromParenthesizedExpression(paren, lang, ctx, arena)
	if !ok {
		return nil, nil, false
	}
	typeArgs := newParentNodeInArena(arena, ctx.typeArgumentsSym, ctx.typeArgumentsName, typeArgChildren, nil, 0)
	argPart := newParentNodeInArena(arena, ctx.argumentPartSym, ctx.argumentPartNamed, []*Node{typeArgs, arguments}, nil, 0)
	selector := newParentNodeInArena(arena, ctx.selectorSym, ctx.selectorNamed, []*Node{argPart}, nil, 0)
	return cloneTreeNodesIntoArena(callee, arena), selector, true
}

func dartComplexTypeArgumentChildren(left, lessOp, greaterOp *Node, source []byte, lang *Language, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) ([]*Node, bool) {
	lessTok := dartRelationalOperatorToken(lessOp, lang, "<")
	greaterTok := dartRelationalOperatorToken(greaterOp, lang, ">")
	if lessTok == nil || greaterTok == nil {
		return nil, false
	}
	children := []*Node{cloneTreeNodesIntoArena(lessTok, arena)}
	hasSelectorTypeArguments := false
	for i := 2; i < resultChildCount(left); i++ {
		part := resultChildAt(left, i)
		converted, selectorTypeArgs, ok := dartComplexTypeArgumentPartChildren(part, source, lang, ctx, arena)
		if !ok {
			return nil, false
		}
		hasSelectorTypeArguments = hasSelectorTypeArguments || selectorTypeArgs
		children = append(children, converted...)
	}
	if !hasSelectorTypeArguments {
		return nil, false
	}
	children = append(children, cloneTreeNodesIntoArena(greaterTok, arena))
	return children, true
}

func dartComplexTypeArgumentPartChildren(part *Node, source []byte, lang *Language, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) ([]*Node, bool, bool) {
	if part == nil {
		return nil, false, false
	}
	switch part.Type(lang) {
	case "identifier", "type_identifier":
		return []*Node{dartCloneAsTypeIdentifier(part, ctx, arena)}, false, true
	case "selector":
		if resultChildCount(part) != 1 {
			return nil, false, false
		}
		inner := resultChildAt(part, 0)
		if inner == nil {
			return nil, false, false
		}
		switch inner.Type(lang) {
		case "type_arguments":
			if !dartTypeArgumentsContainPreFunctionTypeArguments(inner, source, lang) {
				return nil, false, false
			}
			return []*Node{cloneTreeNodesIntoArena(inner, arena)}, true, true
		case "unconditional_assignable_selector":
			if resultChildCount(inner) != 2 {
				return nil, false, false
			}
			dot := resultChildAt(inner, 0)
			ident := resultChildAt(inner, 1)
			if dot == nil || ident == nil || dot.Type(lang) != "." || ident.Type(lang) != "identifier" {
				return nil, false, false
			}
			return []*Node{
				cloneTreeNodesIntoArena(dot, arena),
				dartCloneAsTypeIdentifier(ident, ctx, arena),
			}, false, true
		default:
			return nil, false, false
		}
	default:
		return nil, false, false
	}
}

func dartTypeArgumentsContainPreFunctionTypeArguments(typeArgs *Node, source []byte, lang *Language) bool {
	if typeArgs == nil || lang == nil || typeArgs.Type(lang) != "type_arguments" {
		return false
	}
	for i := 0; i < resultChildCount(typeArgs); i++ {
		child := resultChildAt(typeArgs, i)
		if child != nil && child.Type(lang) == "function_type" && dartFunctionTypeHasGenericReturnType(child, source, lang) {
			return true
		}
	}
	return false
}

func dartFunctionTypeHasGenericReturnType(fn *Node, source []byte, lang *Language) bool {
	return dartFunctionTypeHasReturnTypeArguments(fn, lang)
}

func dartFunctionTypeHasReturnTypeArguments(fn *Node, lang *Language) bool {
	if fn == nil || lang == nil || fn.Type(lang) != "function_type" {
		return false
	}
	for i := 0; i < resultChildCount(fn); i++ {
		child := resultChildAt(fn, i)
		if child == nil {
			continue
		}
		switch child.Type(lang) {
		case "Function":
			return false
		case "type_arguments":
			return true
		}
	}
	return false
}

func dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(typeArgs *Node, lang *Language) bool {
	if typeArgs == nil || lang == nil || typeArgs.Type(lang) != "type_arguments" {
		return false
	}
	for i := 0; i < resultChildCount(typeArgs); i++ {
		child := resultChildAt(typeArgs, i)
		if child == nil {
			continue
		}
		switch child.Type(lang) {
		case "function_type":
			return !dartFunctionTypeHasReturnTypeArguments(child, lang)
		case "type_arguments":
			if dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(child, lang) {
				return true
			}
		case "selector":
			if resultChildCount(child) == 1 && dartTypeArgumentsContainFunctionTypeWithoutGenericReturn(resultChildAt(child, 0), lang) {
				return true
			}
		}
	}
	return false
}

func dartRelationalPartsFromDartTypeArguments(typeArgs *Node, lang *Language, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) (*Node, []*Node, *Node, bool) {
	if typeArgs == nil || lang == nil || typeArgs.Type(lang) != "type_arguments" || resultChildCount(typeArgs) < 3 {
		return nil, nil, nil, false
	}
	lt := resultChildAt(typeArgs, 0)
	gt := resultChildAt(typeArgs, resultChildCount(typeArgs)-1)
	if lt == nil || gt == nil || lt.Type(lang) != "<" || gt.Type(lang) != ">" {
		return nil, nil, nil, false
	}
	parts := make([]*Node, 0, resultChildCount(typeArgs)-2)
	for i := 1; i < resultChildCount(typeArgs)-1; {
		part := resultChildAt(typeArgs, i)
		if part == nil {
			return nil, nil, nil, false
		}
		switch part.Type(lang) {
		case "identifier", "type_identifier":
			parts = append(parts, dartCloneAsIdentifier(part, ctx, arena))
			i++
		case ".":
			if i+1 >= resultChildCount(typeArgs)-1 {
				return nil, nil, nil, false
			}
			next := resultChildAt(typeArgs, i+1)
			if next == nil || next.Type(lang) != "type_identifier" {
				return nil, nil, nil, false
			}
			assignable := newParentNodeInArena(arena, ctx.unconditionalAssignableSelectorSym, ctx.unconditionalAssignableSelectorName, []*Node{
				cloneTreeNodesIntoArena(part, arena),
				dartCloneAsIdentifier(next, ctx, arena),
			}, nil, 0)
			parts = append(parts, newParentNodeInArena(arena, ctx.selectorSym, ctx.selectorNamed, []*Node{assignable}, nil, 0))
			i += 2
		case "type_arguments":
			parts = append(parts, newParentNodeInArena(arena, ctx.selectorSym, ctx.selectorNamed, []*Node{cloneTreeNodesIntoArena(part, arena)}, nil, 0))
			i++
		default:
			return nil, nil, nil, false
		}
	}
	return cloneTreeNodesIntoArena(lt, arena), parts, cloneTreeNodesIntoArena(gt, arena), true
}

func dartArgumentsFromParenthesizedExpression(paren *Node, lang *Language, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) (*Node, bool) {
	if paren == nil || paren.Type(lang) != "parenthesized_expression" || resultChildCount(paren) != 3 {
		return nil, false
	}
	open := resultChildAt(paren, 0)
	argValue := resultChildAt(paren, 1)
	close := resultChildAt(paren, 2)
	if open == nil || argValue == nil || close == nil || open.Type(lang) != "(" || close.Type(lang) != ")" {
		return nil, false
	}
	arg := newParentNodeInArena(arena, ctx.argumentSym, ctx.argumentNamed, []*Node{cloneTreeNodesIntoArena(argValue, arena)}, nil, 0)
	return newParentNodeInArena(arena, ctx.argumentsSym, ctx.argumentsNamed, []*Node{
		cloneTreeNodesIntoArena(open, arena),
		arg,
		cloneTreeNodesIntoArena(close, arena),
	}, nil, 0), true
}

func dartCloneAsTypeIdentifier(n *Node, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) *Node {
	cloned := cloneTreeNodesIntoArena(n, arena)
	if cloned == nil {
		return nil
	}
	cloned.symbol = ctx.typeIdentifierSym
	cloned.setNamed(ctx.typeIdentifier)
	return cloned
}

func dartCloneAsIdentifier(n *Node, ctx dartComplexTypeArgumentFreeCallContext, arena *nodeArena) *Node {
	cloned := cloneTreeNodesIntoArena(n, arena)
	if cloned == nil {
		return nil
	}
	cloned.symbol = ctx.identifierSym
	cloned.setNamed(ctx.identifierNamed)
	return cloned
}

func dartRelationalOperatorWrapsToken(n *Node, lang *Language, want string) bool {
	return dartRelationalOperatorToken(n, lang, want) != nil
}

func dartRelationalOperatorToken(n *Node, lang *Language, want string) *Node {
	if n == nil || lang == nil || n.Type(lang) != "relational_operator" || resultChildCount(n) != 1 {
		return nil
	}
	child := resultChildAt(n, 0)
	if child == nil || child.Type(lang) != want {
		return nil
	}
	return child
}

func normalizeDartConstructorSignatureKinds(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "dart" {
		return
	}
	constructorSym, ok := lang.SymbolByName("constructor_signature")
	if !ok {
		return
	}
	parametersID, _ := lang.FieldByName("parameters")
	constructorNamed := symbolIsNamed(lang, constructorSym)
	walkResultTree(root, func(n *Node) {
		if dartConstructorOwnerType(n.Type(lang)) {
			className := n.ChildByFieldName("name", lang)
			body := n.ChildByFieldName("body", lang)
			if className != nil && body != nil {
				classText := className.Text(source)
				for _, member := range body.children {
					sig := dartConstructorCandidateSignature(member, lang)
					if sig == nil || len(sig.children) != 2 {
						continue
					}
					name := sig.children[0]
					params := sig.children[1]
					if name == nil || params == nil || name.Type(lang) != "identifier" || params.Type(lang) != "formal_parameter_list" {
						continue
					}
					if name.Text(source) != classText {
						continue
					}
					sig.symbol = constructorSym
					sig.setNamed(constructorNamed)
					if len(sig.fieldIDs()) != len(sig.children) {
						ensureNodeFieldStorage(sig, len(sig.children))
					}
					if parametersID != 0 && len(sig.fieldIDs()) > 1 {
						sig.fieldIDs()[1] = parametersID
						if len(sig.fieldSources()) == len(sig.children) {
							sig.fieldSources()[1] = fieldSourceDirect
						}
					}
				}
			}
		}
	})
}

func dartConstructorOwnerType(typ string) bool {
	switch typ {
	case "class_definition", "enum_declaration":
		return true
	default:
		return false
	}
}

func dartConstructorCandidateSignature(member *Node, lang *Language) *Node {
	if member == nil || lang == nil {
		return nil
	}
	switch member.Type(lang) {
	case "method_signature", "declaration":
		if resultChildCount(member) != 1 {
			return nil
		}
		sig := resultChildAt(member, 0)
		if sig != nil && sig.Type(lang) == "function_signature" {
			return sig
		}
	}
	return nil
}

func rewriteDartSingleTypeArgumentFreeCall(parent *Node, idx int, lang *Language, relExprSym Symbol, relExprNamed bool, relOpSym Symbol, relOpNamed bool, parenSym Symbol, parenNamed bool) bool {
	if parent == nil || idx < 0 || idx+1 >= len(parent.children) || lang == nil {
		return false
	}
	callee := parent.children[idx]
	selector := parent.children[idx+1]
	if callee == nil || selector == nil || callee.Type(lang) != "identifier" || selector.Type(lang) != "selector" || len(selector.children) != 1 {
		return false
	}
	argPart := selector.children[0]
	if argPart == nil || argPart.Type(lang) != "argument_part" || len(argPart.children) != 2 {
		return false
	}
	typeArgs := argPart.children[0]
	args := argPart.children[1]
	if typeArgs == nil || args == nil || typeArgs.Type(lang) != "type_arguments" || args.Type(lang) != "arguments" {
		return false
	}
	typeIdent, lt, gt, ok := dartSimpleTypeArgumentParts(typeArgs, lang)
	if !ok {
		return false
	}
	if len(args.children) < 2 {
		return false
	}

	arena := parent.ownerArena
	if typeIdent.Type(lang) == "type_identifier" {
		identSym, ok := lang.SymbolByName("identifier")
		if !ok {
			return false
		}
		identNamed := symbolIsNamed(lang, identSym)
		typeIdent = newLeafNodeInArena(arena, identSym, identNamed, typeIdent.startByte, typeIdent.endByte, typeIdent.startPoint, typeIdent.endPoint)
	}
	lessOp := newParentNodeInArena(arena, relOpSym, relOpNamed, []*Node{lt}, nil, 0)
	left := newParentNodeInArena(arena, relExprSym, relExprNamed, []*Node{callee, lessOp, typeIdent}, nil, 0)
	greaterOp := newParentNodeInArena(arena, relOpSym, relOpNamed, []*Node{gt}, nil, 0)
	parenChildren := dartParenthesizedExpressionChildren(args, lang)
	paren := newParentNodeInArena(arena, parenSym, parenNamed, parenChildren, nil, args.productionID)
	outer := newParentNodeInArena(arena, relExprSym, relExprNamed, []*Node{left, greaterOp, paren}, nil, 0)
	replaceChildRangeWithSingleNode(parent, idx, idx+2, outer)
	return true
}

func dartSimpleTypeArgumentParts(typeArgs *Node, lang *Language) (*Node, *Node, *Node, bool) {
	if typeArgs == nil || lang == nil || typeArgs.Type(lang) != "type_arguments" || len(typeArgs.children) < 3 {
		return nil, nil, nil, false
	}
	lt := typeArgs.children[0]
	gt := typeArgs.children[len(typeArgs.children)-1]
	if lt == nil || gt == nil || lt.Type(lang) != "<" || gt.Type(lang) != ">" {
		return nil, nil, nil, false
	}
	if got := typeArgs.NamedChildCount(); got != 1 {
		return nil, nil, nil, false
	}
	typeIdent := typeArgs.NamedChild(0)
	if typeIdent == nil || typeIdent.Type(lang) != "type_identifier" || nodeContainsNamedType(typeIdent, lang, "type_arguments") {
		return nil, nil, nil, false
	}
	return typeIdent, lt, gt, true
}

func nodeContainsNamedType(root *Node, lang *Language, want string) bool {
	if root == nil || lang == nil {
		return false
	}
	for _, child := range root.children {
		if child == nil {
			continue
		}
		if child.Type(lang) == want {
			return true
		}
		if nodeContainsNamedType(child, lang, want) {
			return true
		}
	}
	return false
}

func dartParenthesizedExpressionChildren(args *Node, lang *Language) []*Node {
	if args == nil || lang == nil {
		return nil
	}
	if len(args.children) != 3 {
		return append([]*Node(nil), args.children...)
	}
	open := args.children[0]
	mid := args.children[1]
	close := args.children[2]
	if open == nil || mid == nil || close == nil {
		return append([]*Node(nil), args.children...)
	}
	if mid.Type(lang) != "argument" || len(mid.children) != 1 || mid.children[0] == nil {
		return append([]*Node(nil), args.children...)
	}
	return []*Node{open, mid.children[0], close}
}
func dartProgramChildrenLookComplete(nodes []*Node, lang *Language) bool {
	if len(nodes) == 0 || lang == nil || lang.Name != "dart" {
		return false
	}
	seen := 0
	for _, n := range nodes {
		if n == nil || n.isExtra() {
			continue
		}
		if n.IsNamed() {
			seen++
			continue
		}
		switch n.Type(lang) {
		case ";":
			seen++
		default:
			return false
		}
	}
	return seen > 0
}
