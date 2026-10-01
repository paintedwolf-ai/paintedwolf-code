package gotreesitter

import "bytes"

func csharpRecoverTopLevelStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	if stmt, ok := csharpRecoverTopLevelLocalDeclarationStatementFromRange(source, start, end, p, arena); ok {
		return stmt, true
	}
	if stmt, ok := csharpRecoverTopLevelLocalFunctionStatementFromRange(source, start, end, p, arena); ok {
		return stmt, true
	}
	if stmt, ok := csharpRecoverWrappedStatementNodeFromRange(source, start, end, p, arena); ok {
		return csharpWrapRecoveredStatementAsGlobal(arena, p.language, stmt)
	}
	return nil, false
}

func csharpRecoverWrappedStatementNodeFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	if stmt, ok := csharpRecoverUsingStatementFromRange(source, start, end, p, arena); ok {
		return stmt, true
	}
	if stmt, ok := csharpRecoverRefTypeLocalDeclarationStatementFromRange(source, start, end, p.language, arena); ok {
		return stmt, true
	}
	if csharpLocalDeclarationInitializerNeedsSourceRecovery(source, start, end) {
		if stmt, ok := csharpRecoverLocalDeclarationStatementFromRange(source, start, end, p, arena); ok {
			return stmt, true
		}
	}
	if bytes.Contains(source[start:end], []byte("scoped")) && bytes.Contains(source[start:end], []byte("=>")) {
		if stmt, ok := csharpRecoverScopedLambdaLocalDeclarationStatementFromRange(source, start, end, p.language, arena); ok {
			return stmt, true
		}
		if stmt, ok := csharpRecoverLocalDeclarationStatementFromRange(source, start, end, p, arena); ok {
			return stmt, true
		}
	}
	const prefix = "class __Q { void __M() { "
	const suffix = " } }\n"
	wrapped := make([]byte, 0, len(prefix)+int(end-start)+len(suffix))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[start:end]...)
	wrapped = append(wrapped, suffix...)
	tree, err := p.parseForRecovery(wrapped)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	stmt := csharpExtractRecoveredWrappedMethodStatement(tree.RootNode(), p.language, arena)
	if stmt == nil {
		return csharpRecoverSimpleTypePatternSwitchStatementFromRange(source, start, end, p, arena)
	}
	if !shiftNodeBytes(stmt, int64(start)-int64(len(prefix))) {
		return nil, false
	}
	recomputeNodePointsFromBytes(stmt, source)
	return stmt, true
}

func csharpRecoverTopLevelLocalDeclarationStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	stmt, ok := csharpRecoverLocalDeclarationStatementFromRange(source, start, end, p, arena)
	if !ok {
		return nil, false
	}
	return csharpWrapRecoveredStatementAsGlobal(arena, p.language, stmt)
}

func csharpRecoverLocalDeclarationStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || source[end-1] != ';' {
		return nil, false
	}
	if stmt, ok := csharpRecoverRefLocalDeclarationStatementFromRange(source, start, end, p, arena); ok {
		return stmt, true
	}
	if stmt, ok := csharpRecoverSimpleVarLocalDeclarationStatementFromRange(source, start, end, p, arena); ok {
		return stmt, true
	}
	stmtEnd := end - 1
	eqPos, ok := csharpFindTopLevelAssignment(source, start, stmtEnd)
	if !ok {
		return nil, false
	}
	valueStart := csharpSkipSpaceBytes(source, eqPos+1)
	valueEnd := csharpTrimRightSpaceBytes(source, stmtEnd)
	if valueStart >= valueEnd {
		return nil, false
	}
	const prefix = "class __Q { void __M() { "
	const suffix = " } }\n"
	wrapped := make([]byte, 0, len(prefix)+int(end-start)+len(suffix))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[start:end]...)
	wrapped = append(wrapped, suffix...)
	valueStartInWrapped := uint32(len(prefix)) + (valueStart - start)
	valueEndInWrapped := uint32(len(prefix)) + (valueEnd - start)
	for i := valueStartInWrapped; i < valueEndInWrapped; i++ {
		wrapped[i] = ' '
	}
	wrapped[valueStartInWrapped] = '0'
	tree, err := p.parseForRecovery(wrapped)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	stmt := csharpExtractRecoveredWrappedMethodStatement(tree.RootNode(), p.language, arena)
	if stmt == nil || stmt.Type(p.language) != "local_declaration_statement" {
		return nil, false
	}
	if !shiftNodeBytes(stmt, int64(start)-int64(len(prefix))) {
		return nil, false
	}
	expr, ok := csharpRecoverExpressionNodeFromRange(source, valueStart, valueEnd, p, arena)
	if !ok {
		return nil, false
	}
	if !csharpReplaceRecoveredVariableInitializer(stmt, p.language, expr) {
		return nil, false
	}
	recomputeNodePointsFromBytes(stmt, source)
	return stmt, true
}

func csharpRecoverSimpleVarLocalDeclarationStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || source[end-1] != ';' || !csharpHasKeywordAt(source, start, "var") {
		return nil, false
	}
	stmtEnd := end - 1
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, start)
	if !ok || typeStart != start || string(source[typeStart:typeEnd]) != "var" {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, typeEnd))
	if !ok {
		return nil, false
	}
	eqPos := csharpSkipSpaceBytes(source, nameEnd)
	if eqPos >= stmtEnd || source[eqPos] != '=' {
		return nil, false
	}
	valueStart, valueEnd := csharpTrimSpaceBounds(source, eqPos+1, stmtEnd)
	if valueStart >= valueEnd || !csharpHasKeywordAt(source, valueStart, "from") {
		return nil, false
	}
	value, ok := csharpRecoverExpressionNodeFromRange(source, valueStart, valueEnd, p, arena)
	if !ok {
		return nil, false
	}
	return csharpBuildLocalDeclarationStatementNode(source, p.language, arena, start, end, typeStart, typeEnd, nameStart, nameEnd, eqPos, value)
}

func csharpLocalDeclarationInitializerNeedsSourceRecovery(source []byte, start, end uint32) bool {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || source[end-1] != ';' {
		return false
	}
	stmtEnd := end - 1
	eqPos, ok := csharpFindTopLevelAssignment(source, start, stmtEnd)
	if !ok {
		return false
	}
	valueStart := csharpSkipSpaceBytes(source, eqPos+1)
	return csharpHasKeywordAt(source, valueStart, "from") || csharpHasKeywordAt(source, valueStart, "ref")
}

func csharpRecoverRefLocalDeclarationStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || source[end-1] != ';' || !csharpHasKeywordAt(source, start, "ref") {
		return nil, false
	}
	stmtEnd := end - 1
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, start+3))
	if !ok || string(source[typeStart:typeEnd]) != "var" {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, typeEnd))
	if !ok {
		return nil, false
	}
	eqPos := csharpSkipSpaceBytes(source, nameEnd)
	if eqPos >= stmtEnd || source[eqPos] != '=' {
		return nil, false
	}
	valueStart, valueEnd := csharpTrimSpaceBounds(source, eqPos+1, stmtEnd)
	if valueStart >= valueEnd || !csharpHasKeywordAt(source, valueStart, "ref") {
		return nil, false
	}
	value, ok := csharpRecoverExpressionNodeFromRange(source, valueStart, valueEnd, p, arena)
	if !ok {
		return nil, false
	}
	lang := p.language
	localDeclSym, ok := symbolByName(lang, "local_declaration_statement")
	if !ok {
		return nil, false
	}
	varDeclSym, ok := symbolByName(lang, "variable_declaration")
	if !ok {
		return nil, false
	}
	declaratorSym, ok := symbolByName(lang, "variable_declarator")
	if !ok {
		return nil, false
	}
	refTypeSym, ok := symbolByName(lang, "ref_type")
	if !ok {
		return nil, false
	}
	implicitTypeSym, ok := symbolByName(lang, "implicit_type")
	if !ok {
		return nil, false
	}
	refTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "ref", start, start+3)
	if !ok {
		return nil, false
	}
	typeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "var", typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	eqTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=", eqPos, eqPos+1)
	if !ok {
		return nil, false
	}
	semiTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end)
	if !ok {
		return nil, false
	}
	implicitTypeNamed := symbolIsNamed(lang, implicitTypeSym)
	refTypeNamed := symbolIsNamed(lang, refTypeSym)
	declaratorNamed := symbolIsNamed(lang, declaratorSym)
	varDeclNamed := symbolIsNamed(lang, varDeclSym)
	localDeclNamed := symbolIsNamed(lang, localDeclSym)
	implicitType := newParentNodeInArena(arena, implicitTypeSym, implicitTypeNamed, []*Node{typeTok}, nil, 0)
	refType := newParentNodeInArena(arena, refTypeSym, refTypeNamed, []*Node{refTok, implicitType}, nil, 0)
	nameID, _ := lang.FieldByName("name")
	typeID, _ := lang.FieldByName("type")
	valueID, _ := lang.FieldByName("value")
	declaratorFields := cloneFieldIDSliceInArena(arena, []FieldID{nameID, 0, valueID})
	declarator := newParentNodeInArena(arena, declaratorSym, declaratorNamed, []*Node{nameNode, eqTok, value}, declaratorFields, 0)
	varDeclFields := cloneFieldIDSliceInArena(arena, []FieldID{typeID, 0})
	varDecl := newParentNodeInArena(arena, varDeclSym, varDeclNamed, []*Node{refType, declarator}, varDeclFields, 0)
	stmt := newParentNodeInArena(arena, localDeclSym, localDeclNamed, []*Node{varDecl, semiTok}, nil, 0)
	recomputeNodePointsFromBytes(stmt, source)
	return stmt, true
}

func csharpRecoverRefTypeLocalDeclarationStatementFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || source[end-1] != ';' || !csharpHasKeywordAt(source, start, "ref") {
		return nil, false
	}
	typeStart := csharpSkipSpaceBytes(source, start+3)
	typeNameStart, typeNameEnd, ok := csharpScanIdentifierAt(source, typeStart)
	if !ok || typeNameStart != typeStart {
		return nil, false
	}
	starPos := csharpSkipSpaceBytes(source, typeNameEnd)
	if starPos >= end || source[starPos] != '*' {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, starPos+1))
	if !ok || csharpSkipSpaceBytes(source, nameEnd) != end-1 {
		return nil, false
	}
	localDeclSym, ok := symbolByName(lang, "local_declaration_statement")
	if !ok {
		return nil, false
	}
	varDeclSym, ok := symbolByName(lang, "variable_declaration")
	if !ok {
		return nil, false
	}
	declaratorSym, ok := symbolByName(lang, "variable_declarator")
	if !ok {
		return nil, false
	}
	refTypeSym, ok := symbolByName(lang, "ref_type")
	if !ok {
		return nil, false
	}
	pointerTypeSym, ok := symbolByName(lang, "pointer_type")
	if !ok {
		return nil, false
	}
	refTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "ref", start, start+3)
	if !ok {
		return nil, false
	}
	typeNode, ok := csharpBuildTypeNameNodeFromSource(arena, source, lang, typeNameStart, typeNameEnd)
	if !ok {
		return nil, false
	}
	starTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "*", starPos, starPos+1)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	semiTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end)
	if !ok {
		return nil, false
	}
	pointerTypeNamed := symbolIsNamed(lang, pointerTypeSym)
	refTypeNamed := symbolIsNamed(lang, refTypeSym)
	declaratorNamed := symbolIsNamed(lang, declaratorSym)
	varDeclNamed := symbolIsNamed(lang, varDeclSym)
	localDeclNamed := symbolIsNamed(lang, localDeclSym)
	pointerType := newParentNodeInArena(arena, pointerTypeSym, pointerTypeNamed, []*Node{typeNode, starTok}, nil, 0)
	refType := newParentNodeInArena(arena, refTypeSym, refTypeNamed, []*Node{refTok, pointerType}, nil, 0)
	nameID, _ := lang.FieldByName("name")
	typeID, _ := lang.FieldByName("type")
	declaratorFields := cloneFieldIDSliceInArena(arena, []FieldID{nameID})
	declarator := newParentNodeInArena(arena, declaratorSym, declaratorNamed, []*Node{nameNode}, declaratorFields, 0)
	varDeclFields := cloneFieldIDSliceInArena(arena, []FieldID{typeID, 0})
	varDecl := newParentNodeInArena(arena, varDeclSym, varDeclNamed, []*Node{refType, declarator}, varDeclFields, 0)
	stmt := newParentNodeInArena(arena, localDeclSym, localDeclNamed, []*Node{varDecl, semiTok}, nil, 0)
	recomputeNodePointsFromBytes(stmt, source)
	return stmt, true
}

func csharpRecoverScopedLambdaLocalDeclarationStatementFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || source[end-1] != ';' {
		return nil, false
	}
	stmtEnd := end - 1
	varStart, varEnd, ok := csharpScanIdentifierAt(source, start)
	if !ok || varStart != start || string(source[varStart:varEnd]) != "var" {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, varEnd))
	if !ok {
		return nil, false
	}
	eqPos := csharpSkipSpaceBytes(source, nameEnd)
	if eqPos >= stmtEnd || source[eqPos] != '=' {
		return nil, false
	}
	valueStart, valueEnd := csharpTrimSpaceBounds(source, eqPos+1, stmtEnd)
	if valueStart >= valueEnd {
		return nil, false
	}
	lambda, ok := csharpRecoverQueryExpressionNodeFromRange(source, valueStart, valueEnd, lang, arena)
	if !ok || lambda.Type(lang) != "lambda_expression" {
		return nil, false
	}
	return csharpBuildLocalDeclarationStatementNode(source, lang, arena, start, end, varStart, varEnd, nameStart, nameEnd, eqPos, lambda)
}

func csharpBuildLocalDeclarationStatementNode(source []byte, lang *Language, arena *nodeArena, start, end, typeStart, typeEnd, nameStart, nameEnd, eqPos uint32, value *Node) (*Node, bool) {
	if lang == nil || arena == nil || value == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	localDeclSym, ok := symbolByName(lang, "local_declaration_statement")
	if !ok {
		return nil, false
	}
	varDeclSym, ok := symbolByName(lang, "variable_declaration")
	if !ok {
		return nil, false
	}
	declaratorSym, ok := symbolByName(lang, "variable_declarator")
	if !ok {
		return nil, false
	}
	implicitTypeSym, ok := symbolByName(lang, "implicit_type")
	if !ok {
		return nil, false
	}
	typeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "var", typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	eqTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=", eqPos, eqPos+1)
	if !ok {
		return nil, false
	}
	semiTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end)
	if !ok {
		return nil, false
	}

	implicitTypeNamed := symbolIsNamed(lang, implicitTypeSym)
	declaratorNamed := symbolIsNamed(lang, declaratorSym)
	varDeclNamed := symbolIsNamed(lang, varDeclSym)
	localDeclNamed := symbolIsNamed(lang, localDeclSym)
	typeNode := newParentNodeInArena(arena, implicitTypeSym, implicitTypeNamed, []*Node{typeTok}, nil, 0)
	nameID, _ := lang.FieldByName("name")
	typeID, _ := lang.FieldByName("type")
	valueID, _ := lang.FieldByName("value")
	declaratorFields := cloneFieldIDSliceInArena(arena, []FieldID{nameID, 0, valueID})
	declarator := newParentNodeInArena(arena, declaratorSym, declaratorNamed, []*Node{nameNode, eqTok, value}, declaratorFields, 0)
	varDeclFields := cloneFieldIDSliceInArena(arena, []FieldID{typeID, 0})
	varDecl := newParentNodeInArena(arena, varDeclSym, varDeclNamed, []*Node{typeNode, declarator}, varDeclFields, 0)
	return newParentNodeInArena(arena, localDeclSym, localDeclNamed, []*Node{varDecl, semiTok}, nil, 0), true
}

func csharpRecoverTopLevelLocalFunctionStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || source[end-1] != '}' {
		return nil, false
	}
	openRel := 0
	for openRel < int(end-start) && source[start+uint32(openRel)] != '{' {
		openRel++
	}
	if start+uint32(openRel) >= end || source[start+uint32(openRel)] != '{' {
		return nil, false
	}
	openBrace := start + uint32(openRel)
	closeBrace := csharpFindMatchingBraceByte(source, int(openBrace), int(end))
	if closeBrace < 0 || uint32(closeBrace+1) != end {
		return nil, false
	}
	const prefix = "class __Q { "
	const suffix = " }\n"
	wrapped := make([]byte, 0, len(prefix)+int(end-start)+len(suffix))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[start:end]...)
	wrapped = append(wrapped, suffix...)
	bodyStart := uint32(len(prefix)) + (openBrace - start)
	bodyEnd := uint32(len(prefix)) + (uint32(closeBrace) - start)
	for i := bodyStart + 1; i < bodyEnd; i++ {
		wrapped[i] = ' '
	}
	tree, err := p.parseForRecovery(wrapped)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	fn := csharpExtractRecoveredWrappedClassMethod(tree.RootNode(), p.language, arena)
	if fn == nil {
		return nil, false
	}
	if !shiftNodeBytes(fn, int64(start)-int64(len(prefix))) {
		return nil, false
	}
	statements, ok := csharpRecoverMethodBlockStatementsFromRange(source, openBrace+1, uint32(closeBrace), p, arena)
	if !ok {
		return nil, false
	}
	block, ok := csharpBuildRecoveredMethodBlockNode(source, p.language, arena, openBrace, uint32(closeBrace), statements)
	if !ok {
		return nil, false
	}
	if !csharpReplaceMethodBlock(fn, p.language, block) {
		return nil, false
	}
	if !csharpConvertMethodToLocalFunctionStatement(fn, p.language) {
		return nil, false
	}
	recomputeNodePointsFromBytes(fn, source)
	return csharpWrapRecoveredStatementAsGlobal(arena, p.language, fn)
}

func csharpExtractRecoveredWrappedMethodStatement(root *Node, lang *Language, arena *nodeArena) *Node {
	if root == nil || lang == nil {
		return nil
	}
	method := csharpFindFirstNamedDescendantOfType(root, lang, "method_declaration")
	if method == nil {
		return nil
	}
	block := csharpFindFirstNamedDescendantOfType(method, lang, "block")
	if block == nil {
		return nil
	}
	var candidate *Node
	for _, child := range block.children {
		if child == nil || !child.IsNamed() || !csharpIsRecoveredMethodBlockStatement(child, lang) {
			continue
		}
		if candidate != nil {
			return nil
		}
		if arena != nil {
			candidate = cloneTreeNodesIntoArena(child, arena)
		} else {
			candidate = child
		}
	}
	return candidate
}

func csharpRecoverSimpleTypePatternSwitchStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || !csharpHasKeywordAt(source, start, "switch") {
		return nil, false
	}
	openParen := csharpSkipSpaceBytes(source, start+uint32(len("switch")))
	if openParen >= end || source[openParen] != '(' {
		return nil, false
	}
	closeParen, ok := csharpFindMatchingParenByte(source, openParen, end)
	if !ok {
		return nil, false
	}
	exprStart, exprEnd := csharpTrimSpaceBounds(source, openParen+1, closeParen)
	expr, ok := csharpRecoverExpressionNodeFromRange(source, exprStart, exprEnd, p, arena)
	if !ok {
		return nil, false
	}
	openBrace := csharpSkipSpaceBytes(source, closeParen+1)
	if openBrace >= end || source[openBrace] != '{' {
		return nil, false
	}
	closeBrace := csharpFindMatchingBraceByte(source, int(openBrace), int(end))
	if closeBrace < 0 || uint32(closeBrace+1) != end {
		return nil, false
	}
	bodyStart, bodyEnd := csharpTrimSpaceBounds(source, openBrace+1, uint32(closeBrace))
	if bodyStart >= bodyEnd || !csharpHasKeywordAt(source, bodyStart, "case") {
		return nil, false
	}
	patternStart := csharpSkipSpaceBytes(source, bodyStart+uint32(len("case")))
	whenPos, ok := csharpFindKeywordAfter(source, patternStart, bodyEnd, "when")
	if !ok {
		return nil, false
	}
	patternEnd := csharpTrimRightSpaceBytes(source, whenPos)
	colonPos, ok := csharpFindTopLevelOperator(source, whenPos+uint32(len("when")), bodyEnd, ":")
	if !ok {
		return nil, false
	}
	conditionStart, conditionEnd := csharpTrimSpaceBounds(source, whenPos+uint32(len("when")), colonPos)
	if conditionStart >= conditionEnd {
		return nil, false
	}
	stmtStart := csharpSkipSpaceBytes(source, colonPos+1)
	if !csharpHasKeywordAt(source, stmtStart, "break") {
		return nil, false
	}
	semiPos, ok := csharpFindTopLevelOperator(source, stmtStart, bodyEnd, ";")
	if !ok {
		return nil, false
	}
	pattern, ok := csharpBuildSimpleTypePatternNode(source, patternStart, patternEnd, p.language, arena)
	if !ok {
		return nil, false
	}
	condition, ok := csharpBuildLessThanBinaryExpressionNode(source, conditionStart, conditionEnd, p, arena)
	if !ok {
		return nil, false
	}
	whenClause, ok := csharpBuildWhenClauseNode(source, whenPos, colonPos, condition, p.language, arena)
	if !ok {
		return nil, false
	}
	breakStmt, ok := csharpBuildBreakStatementNode(source, stmtStart, semiPos+1, p.language, arena)
	if !ok {
		return nil, false
	}
	section, ok := csharpBuildSwitchSectionNode(source, bodyStart, colonPos, p.language, arena, pattern, whenClause, breakStmt)
	if !ok {
		return nil, false
	}
	body, ok := csharpBuildSwitchBodyNode(source, openBrace, uint32(closeBrace), p.language, arena, []*Node{section})
	if !ok {
		return nil, false
	}
	return csharpBuildSwitchStatementNode(source, start, openParen, closeParen, openBrace, p.language, arena, expr, body)
}

func csharpExtractRecoveredWrappedClassMethod(root *Node, lang *Language, arena *nodeArena) *Node {
	if root == nil || lang == nil {
		return nil
	}
	classDecl := csharpFindFirstNamedDescendantOfType(root, lang, "class_declaration")
	if classDecl == nil {
		return nil
	}
	var candidate *Node
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil || candidate != nil {
			return
		}
		if n.IsNamed() && n.Type(lang) == "method_declaration" {
			if arena != nil {
				candidate = cloneTreeNodesIntoArena(n, arena)
			} else {
				candidate = n
			}
			return
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	walk(classDecl)
	return candidate
}

func csharpRecoverMethodBlockStatementsFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) ([]*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start > end || int(end) > len(source) {
		return nil, false
	}
	if start == end {
		return nil, true
	}
	if bytesAreTrivia(source[start:end]) {
		return nil, true
	}
	relSpans := csharpTopLevelChunkSpans(source[start:end])
	if len(relSpans) == 0 {
		return nil, false
	}
	out := make([]*Node, 0, len(relSpans))
	for _, rel := range relSpans {
		spanStart := start + rel[0]
		spanEnd := start + rel[1]
		for _, part := range csharpSplitLeadingTopLevelCommentSpans(source, spanStart, spanEnd) {
			if comment, ok := csharpRecoverTopLevelCommentNodeFromRange(source, part[0], part[1], p.language, arena); ok {
				out = append(out, comment)
				continue
			}
			stmt, ok := csharpRecoverWrappedStatementNodeFromRange(source, part[0], part[1], p, arena)
			if !ok {
				stmt, ok = csharpRecoverOpaqueMethodStatementFromRange(source, part[0], part[1], p.language, arena)
				if !ok {
					return nil, false
				}
			}
			out = append(out, stmt)
		}
	}
	return out, true
}

func normalizeCSharpRecoveredMethodBlocks(root *Node, source []byte, p *Parser) {
	if root == nil || p == nil || p.language == nil || p.language.Name != "c_sharp" || len(source) == 0 {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(p.language) == "block" && csharpBlockNeedsSourceStatementRecovery(n, source, p.language) {
			if recovered, ok := csharpRecoverMethodBlockFromSource(n, source, p); ok {
				csharpReplaceNodeContents(n, recovered)
			}
		}
	})
}

func csharpBlockNeedsSourceStatementRecovery(block *Node, source []byte, lang *Language) bool {
	if block == nil || lang == nil || block.Type(lang) != "block" ||
		block.startByte >= block.endByte || int(block.endByte) > len(source) ||
		block.endByte-block.startByte > csharpMaxTopLevelChunkRecoverySourceBytes {
		return false
	}
	for _, child := range block.children {
		if csharpStatementNeedsSourceRecovery(child, source, lang) {
			return true
		}
	}
	return false
}

func csharpStatementsNeedSourceRecovery(statements []*Node, source []byte, lang *Language) bool {
	for _, stmt := range statements {
		if csharpStatementNeedsSourceRecovery(stmt, source, lang) {
			return true
		}
	}
	return false
}

func csharpStatementNeedsSourceRecovery(stmt *Node, source []byte, lang *Language) bool {
	if stmt == nil || lang == nil || stmt.Type(lang) != "expression_statement" ||
		stmt.startByte >= stmt.endByte || int(stmt.endByte) > len(source) {
		return false
	}
	var named []*Node
	for _, child := range stmt.children {
		if child != nil && child.IsNamed() {
			named = append(named, child)
		}
	}
	if len(named) != 1 || named[0] == nil || named[0].Type(lang) != "identifier" {
		return false
	}
	ident := named[0]
	exprStart, exprEnd := csharpTrimSpaceBounds(source, stmt.startByte, stmt.endByte)
	if exprEnd > exprStart && source[exprEnd-1] == ';' {
		exprEnd = csharpTrimRightSpaceBytes(source, exprEnd-1)
	}
	if exprStart != ident.startByte || exprEnd != ident.endByte {
		return true
	}
	return !csharpSourceSpanIsSimpleIdentifier(source, ident.startByte, ident.endByte)
}

func csharpSourceSpanIsSimpleIdentifier(source []byte, start, end uint32) bool {
	identStart, identEnd, ok := csharpScanIdentifierAt(source, start)
	return ok && identStart == start && identEnd == end
}

func csharpRecoverMethodBlockFromSource(block *Node, source []byte, p *Parser) (*Node, bool) {
	if block == nil || p == nil || p.language == nil || block.ownerArena == nil ||
		block.startByte >= block.endByte || int(block.endByte) > len(source) ||
		block.endByte-block.startByte > csharpMaxTopLevelChunkRecoverySourceBytes {
		return nil, false
	}
	openBrace := block.startByte
	if source[openBrace] != '{' {
		found := false
		for i := block.startByte; i < block.endByte && int(i) < len(source); i++ {
			if source[i] == '{' {
				openBrace = i
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	closeBrace := block.endByte - 1
	if source[closeBrace] != '}' {
		found := false
		for i := block.endByte; i > openBrace; i-- {
			if source[i-1] == '}' {
				closeBrace = i - 1
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	statements, ok := csharpRecoverMethodBlockStatementsFromRange(source, openBrace+1, closeBrace, p, block.ownerArena)
	if !ok {
		return nil, false
	}
	return csharpBuildRecoveredMethodBlockNode(source, p.language, block.ownerArena, openBrace, closeBrace, statements)
}

func csharpRecoverUsingStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end || int(end) > len(source) || !csharpHasKeywordAt(source, start, "using") {
		return nil, false
	}
	openParen := csharpSkipSpaceBytes(source, start+5)
	if openParen >= end || source[openParen] != '(' {
		return nil, false
	}
	closeParen, ok := csharpFindMatchingParenByte(source, openParen, end)
	if !ok {
		return nil, false
	}
	blockStart := csharpSkipSpaceBytes(source, closeParen+1)
	if blockStart >= end || source[blockStart] != '{' {
		return nil, false
	}
	blockEnd := csharpFindMatchingBraceByte(source, int(blockStart), int(end))
	if blockEnd < 0 || uint32(blockEnd+1) != end {
		return nil, false
	}
	decl, ok := csharpBuildUsingVariableDeclarationNode(source, openParen+1, closeParen, p.language, arena)
	if !ok {
		return nil, false
	}
	statements, ok := csharpRecoverMethodBlockStatementsFromRange(source, blockStart+1, uint32(blockEnd), p, arena)
	if !ok {
		return nil, false
	}
	block, ok := csharpBuildRecoveredMethodBlockNode(source, p.language, arena, blockStart, uint32(blockEnd), statements)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(p.language, "using_statement")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(p.language, sym)
	usingTok, ok := csharpBuildLeafNodeByName(arena, source, p.language, "using", start, start+5)
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, p.language, "(", openParen, openParen+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, p.language, ")", closeParen, closeParen+1)
	if !ok {
		return nil, false
	}
	children := []*Node{usingTok, openTok, decl, closeTok, block}
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	stmt := newParentNodeInArena(arena, sym, named, buf, nil, 0)
	recomputeNodePointsFromBytes(stmt, source)
	return stmt, true
}

func csharpBuildUsingVariableDeclarationNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	typeStart, typeEnd, ok := csharpScanIdentifierAt(source, start)
	if !ok || typeStart != start {
		return nil, false
	}
	typeNode, ok := csharpBuildTypeNameNodeFromSource(arena, source, lang, typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	cursor := csharpSkipSpaceBytes(source, typeEnd)
	items := csharpSplitTopLevelByComma(source, cursor, end)
	if len(items) == 0 {
		return nil, false
	}
	declarators := make([]*Node, 0, len(items))
	for _, span := range items {
		nameStart, nameEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, span[0]))
		if !ok {
			return nil, false
		}
		eqPos := csharpSkipSpaceBytes(source, nameEnd)
		if eqPos >= span[1] || source[eqPos] != '=' {
			return nil, false
		}
		valueStart, valueEnd := csharpTrimSpaceBounds(source, eqPos+1, span[1])
		if valueStart >= valueEnd {
			return nil, false
		}
		value, ok := csharpRecoverQueryExpressionNodeFromRange(source, valueStart, valueEnd, lang, arena)
		if !ok {
			return nil, false
		}
		declarator, ok := csharpBuildVariableDeclaratorNode(source, lang, arena, nameStart, nameEnd, eqPos, value)
		if !ok {
			return nil, false
		}
		declarators = append(declarators, declarator)
	}
	varDeclSym, ok := symbolByName(lang, "variable_declaration")
	if !ok {
		return nil, false
	}
	children := make([]*Node, 0, 1+len(declarators))
	children = append(children, typeNode)
	children = append(children, declarators...)
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	typeID, _ := lang.FieldByName("type")
	fields := make([]FieldID, len(children))
	fields[0] = typeID
	fieldIDs := cloneFieldIDSliceInArena(arena, fields)
	named := symbolIsNamed(lang, varDeclSym)
	return newParentNodeInArena(arena, varDeclSym, named, buf, fieldIDs, 0), true
}

func csharpBuildVariableDeclaratorNode(source []byte, lang *Language, arena *nodeArena, nameStart, nameEnd, eqPos uint32, value *Node) (*Node, bool) {
	declaratorSym, ok := symbolByName(lang, "variable_declarator")
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	eqTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=", eqPos, eqPos+1)
	if !ok {
		return nil, false
	}
	nameID, _ := lang.FieldByName("name")
	valueID, _ := lang.FieldByName("value")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{nameID, 0, valueID})
	named := symbolIsNamed(lang, declaratorSym)
	return newParentNodeInArena(arena, declaratorSym, named, []*Node{nameNode, eqTok, value}, fields, 0), true
}

func csharpRecoverOpaqueMethodStatementFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	exprEnd := end
	if exprEnd > start && source[exprEnd-1] == ';' {
		exprEnd = csharpTrimRightSpaceBytes(source, exprEnd-1)
	}
	if exprEnd <= start {
		exprEnd = end
	}
	exprSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return nil, false
	}
	stmtSym, ok := symbolByName(lang, "expression_statement")
	if !ok {
		return nil, false
	}
	exprNamed := symbolIsNamed(lang, exprSym)
	stmtNamed := symbolIsNamed(lang, stmtSym)
	expr := newLeafNodeInArena(arena, exprSym, exprNamed, start, exprEnd, advancePointByBytes(Point{}, source[:start]), advancePointByBytes(Point{}, source[:exprEnd]))
	children := []*Node{expr}
	if semi, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end); ok && source[end-1] == ';' {
		children = append(children, semi)
	}
	buf := arena.allocNodeSlice(len(children))
	copy(buf, children)
	return newParentNodeInArena(arena, stmtSym, stmtNamed, buf, nil, 0), true
}

func csharpFindFirstNamedDescendantOfType(root *Node, lang *Language, want string) *Node {
	if root == nil || lang == nil {
		return nil
	}
	if root.IsNamed() && root.Type(lang) == want {
		return root
	}
	for _, child := range root.children {
		if got := csharpFindFirstNamedDescendantOfType(child, lang, want); got != nil {
			return got
		}
	}
	return nil
}

func csharpWrapRecoveredStatementAsGlobal(arena *nodeArena, lang *Language, stmt *Node) (*Node, bool) {
	if lang == nil || stmt == nil {
		return nil, false
	}
	sym, ok := symbolByName(lang, "global_statement")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	children := []*Node{stmt}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	global := newParentNodeInArena(arena, sym, named, children, nil, 0)
	global.setHasError(false)
	return global, true
}

func csharpBuildSimpleTypePatternNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	var typeNode *Node
	var ok bool
	if typeNode, ok = csharpBuildLeafNodeByName(arena, source, lang, "predefined_type", start, end); !ok {
		typeNode, ok = csharpBuildIdentifierNodeFromSource(source, start, end, lang, arena)
		if !ok {
			return nil, false
		}
	}
	sym, ok := symbolByName(lang, "type_pattern")
	if !ok {
		return nil, false
	}
	typeID, _ := lang.FieldByName("type")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{typeID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{typeNode}, fields, 0), true
}

func csharpBuildLessThanBinaryExpressionNode(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	opPos, ok := csharpFindTopLevelOperator(source, start, end, "<")
	if !ok {
		return nil, false
	}
	return csharpBuildBinaryExpressionNode(arena, source, p.language, start, opPos, opPos+1, end)
}

func csharpBuildWhenClauseNode(source []byte, whenPos, colonPos uint32, condition *Node, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || condition == nil || whenPos >= colonPos || int(colonPos) > len(source) {
		return nil, false
	}
	sym, ok := symbolByName(lang, "when_clause")
	if !ok {
		return nil, false
	}
	whenTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "when", whenPos, whenPos+uint32(len("when")))
	if !ok {
		return nil, false
	}
	valueID, _ := lang.FieldByName("value")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{0, valueID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{whenTok, condition}, fields, 0), true
}

func csharpBuildBreakStatementNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	sym, ok := symbolByName(lang, "break_statement")
	if !ok {
		return nil, false
	}
	breakTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "break", start, start+uint32(len("break")))
	if !ok {
		return nil, false
	}
	semiTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ";", end-1, end)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{breakTok, semiTok}, nil, 0), true
}

func csharpBuildSwitchSectionNode(source []byte, casePos, colonPos uint32, lang *Language, arena *nodeArena, pattern, whenClause, stmt *Node) (*Node, bool) {
	if lang == nil || pattern == nil || whenClause == nil || stmt == nil || casePos >= colonPos || int(colonPos) > len(source) {
		return nil, false
	}
	sym, ok := symbolByName(lang, "switch_section")
	if !ok {
		return nil, false
	}
	caseTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "case", casePos, casePos+uint32(len("case")))
	if !ok {
		return nil, false
	}
	colonTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ":", colonPos, colonPos+1)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{caseTok, pattern, whenClause, colonTok, stmt}, nil, 0), true
}

func csharpBuildSwitchBodyNode(source []byte, openBrace, closeBrace uint32, lang *Language, arena *nodeArena, sections []*Node) (*Node, bool) {
	if lang == nil || openBrace >= closeBrace || int(closeBrace+1) > len(source) {
		return nil, false
	}
	sym, ok := symbolByName(lang, "switch_body")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "{", openBrace, openBrace+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "}", closeBrace, closeBrace+1)
	if !ok {
		return nil, false
	}
	children := make([]*Node, 0, len(sections)+2)
	children = append(children, openTok)
	children = append(children, sections...)
	children = append(children, closeTok)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildSwitchStatementNode(source []byte, switchPos, openParen, closeParen, openBrace uint32, lang *Language, arena *nodeArena, expr, body *Node) (*Node, bool) {
	if lang == nil || expr == nil || body == nil || switchPos >= openParen || openParen >= closeParen || int(closeParen+1) > len(source) {
		return nil, false
	}
	sym, ok := symbolByName(lang, "switch_statement")
	if !ok {
		return nil, false
	}
	switchTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "switch", switchPos, switchPos+uint32(len("switch")))
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "(", openParen, openParen+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ")", closeParen, closeParen+1)
	if !ok {
		return nil, false
	}
	expressionID, _ := lang.FieldByName("expression")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{0, 0, expressionID, 0, 0})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{switchTok, openTok, expr, closeTok, body}, fields, 0), true
}

func csharpReplaceMethodBlock(method *Node, lang *Language, block *Node) bool {
	if method == nil || lang == nil || block == nil || method.Type(lang) != "method_declaration" {
		return false
	}
	for i := len(method.children) - 1; i >= 0; i-- {
		if method.children[i] == nil || method.children[i].Type(lang) != "block" {
			continue
		}
		method.children[i] = block
		block.parent = method
		block.childIndex = int32(i)
		method.setHasError(false)
		populateParentNode(method, method.children)
		return true
	}
	return false
}

func csharpReplaceRecoveredVariableInitializer(root *Node, lang *Language, expr *Node) bool {
	if root == nil || lang == nil || expr == nil {
		return false
	}
	var replace func(*Node) bool
	replace = func(n *Node) bool {
		if n == nil {
			return false
		}
		if n.Type(lang) == "variable_declarator" && len(n.children) >= 3 {
			idx := len(n.children) - 1
			n.children[idx] = expr
			expr.parent = n
			expr.childIndex = int32(idx)
			n.setHasError(false)
			populateParentNode(n, n.children)
			csharpExtendNodeEndIfNeeded(n, expr.endByte)
			if n.parent != nil {
				csharpExtendNodeEndIfNeeded(n.parent, expr.endByte)
			}
			return true
		}
		for _, child := range n.children {
			if replace(child) {
				return true
			}
		}
		return false
	}
	return replace(root)
}

func csharpExtendNodeEndIfNeeded(n *Node, end uint32) {
	for cur := n; cur != nil; cur = cur.parent {
		if cur.endByte >= end {
			continue
		}
		cur.endByte = end
	}
}

func csharpConvertMethodToLocalFunctionStatement(n *Node, lang *Language) bool {
	if n == nil || lang == nil || n.Type(lang) != "method_declaration" {
		return false
	}
	sym, ok := symbolByName(lang, "local_function_statement")
	if !ok {
		return false
	}
	n.symbol = sym
	n.setNamed(symbolIsNamed(lang, sym))
	n.productionID = 0
	n.setHasError(false)
	populateParentNode(n, n.children)
	return true
}
