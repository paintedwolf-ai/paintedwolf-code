package gotreesitter

func normalizeSQLRecoveredSelectRoot(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "sql" || root.Type(lang) != "source_file" || len(root.children) < 3 {
		return
	}
	if !sqlLooksLikeFlatRecoveredSelect(root, lang) {
		return
	}
	selectStmtSym, ok := symbolByName(lang, "select_statement")
	if !ok {
		return
	}
	selectClauseSym, ok := symbolByName(lang, "select_clause")
	if !ok {
		return
	}
	selectClauseBodySym, ok := symbolByName(lang, "select_clause_body")
	if !ok {
		return
	}
	nullParentSym, ok := findVisibleSymbolByName(lang, "NULL", true)
	if !ok {
		return
	}
	nullLeafSym, ok := findVisibleSymbolByName(lang, "NULL", false)
	if !ok {
		return
	}
	bodyChildren := sqlFlattenRecoveredSelectBody(root.children[1:], nil, lang)
	if !sqlNeedsRecoveredMissingNull(bodyChildren, lang) {
		return
	}
	bodyChildren = append(bodyChildren, sqlRecoveredNullNode(root.ownerArena, bodyChildren[len(bodyChildren)-1], nullParentSym, nullLeafSym))
	bodyChildren = cloneNodeSliceIfArena(root.ownerArena, bodyChildren)
	selectClauseBody := newParentNodeInArena(root.ownerArena, selectClauseBodySym, symbolIsNamed(lang, selectClauseBodySym), bodyChildren, nil, 0)
	selectClause := newParentNodeInArena(root.ownerArena, selectClauseSym, symbolIsNamed(lang, selectClauseSym), []*Node{root.children[0], selectClauseBody}, nil, 0)
	selectStatement := newParentNodeInArena(root.ownerArena, selectStmtSym, symbolIsNamed(lang, selectStmtSym), []*Node{selectClause}, nil, 0)
	root.children = cloneNodeSliceIfArena(root.ownerArena, []*Node{selectStatement})
	root.clearFieldMetadata()
	root.setHasError(selectStatement.HasError())
}

func sqlLooksLikeFlatRecoveredSelect(root *Node, lang *Language) bool {
	if len(root.children) < 3 || root.children[0] == nil || root.children[0].Type(lang) != "SELECT" {
		return false
	}
	sawRepeat := false
	sawBody := false
	for _, child := range root.children[1:] {
		if child == nil {
			continue
		}
		switch child.Type(lang) {
		case "_aliasable_expression", "_expression", ",", "comment":
			if child.Type(lang) != "," && child.Type(lang) != "comment" {
				sawBody = true
			}
			continue
		case "select_clause_body_repeat1":
			sawRepeat = true
			sawBody = true
			continue
		default:
			if !sqlLooksLikeRecoveredSelectBodyNode(child, lang) {
				return false
			}
			sawBody = true
		}
	}
	return sawRepeat && sawBody
}

func sqlFlattenRecoveredSelectBody(nodes []*Node, out []*Node, lang *Language) []*Node {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		switch node.Type(lang) {
		case "_aliasable_expression", "_expression", "select_clause_body_repeat1":
			if len(node.children) > 0 {
				out = sqlFlattenRecoveredSelectBody(node.children, out, lang)
				continue
			}
		}
		out = append(out, node)
	}
	return out
}

func sqlLooksLikeRecoveredSelectBodyNode(node *Node, lang *Language) bool {
	if node == nil || lang == nil || node.isExtra() {
		return false
	}
	typ := node.Type(lang)
	switch typ {
	case "type_cast", "identifier", "function_call", "string", "number", "NULL", "boolean", "dotted_name", "binary_expression":
		return true
	default:
		return false
	}
}

func sqlNeedsRecoveredMissingNull(children []*Node, lang *Language) bool {
	last, prev := sqlLastAndPrevNonNilChild(children)
	if last == nil {
		return false
	}
	if last.Type(lang) == "NULL" {
		return false
	}
	if last.Type(lang) == "comment" && prev != nil && prev.Type(lang) == "," {
		return true
	}
	return last.Type(lang) == ","
}

func sqlLastAndPrevNonNilChild(children []*Node) (last *Node, prev *Node) {
	for i := len(children) - 1; i >= 0; i-- {
		if children[i] == nil {
			continue
		}
		last = children[i]
		for j := i - 1; j >= 0; j-- {
			if children[j] != nil {
				prev = children[j]
				break
			}
		}
		return last, prev
	}
	return nil, nil
}

func sqlRecoveredNullNode(arena *nodeArena, anchor *Node, nullParentSym, nullLeafSym Symbol) *Node {
	if anchor == nil {
		return nil
	}
	leaf := newLeafNodeInArena(arena, nullLeafSym, false, anchor.endByte, anchor.endByte, anchor.endPoint, anchor.endPoint)
	leaf.setMissing(true)
	leaf.setHasError(true)
	node := newParentNodeInArena(arena, nullParentSym, true, []*Node{leaf}, nil, 0)
	node.setHasError(true)
	return node
}

func normalizeSQLTrailingSelectListError(root *Node, lang *Language) {
	if root == nil || lang == nil || lang.Name != "sql" || root.Type(lang) != "source_file" || len(root.children) < 2 {
		return
	}
	stmt := root.children[0]
	if stmt == nil || stmt.Type(lang) != "select_statement" {
		return
	}
	body, selectClause := sqlSelectStatementBody(stmt, lang)
	if body == nil || selectClause == nil || len(body.children) == 0 {
		return
	}

	trailing := make([]*Node, 0, len(root.children)-1+1)
	for _, child := range root.children[1:] {
		switch {
		case child == nil:
			continue
		case child.Type(lang) == "comment":
			trailing = append(trailing, child)
		case child.Type(lang) == ",":
			trailing = append(trailing, child)
		case child.Type(lang) == "ERROR":
			comma := sqlSingleErrorComma(child, lang)
			if comma == nil {
				return
			}
			trailing = append(trailing, comma)
		default:
			return
		}
	}
	if !sqlNeedsRecoveredMissingNull(trailing, lang) {
		return
	}

	nullParentSym, ok := findVisibleSymbolByName(lang, "NULL", true)
	if !ok {
		return
	}
	nullLeafSym, ok := findVisibleSymbolByName(lang, "NULL", false)
	if !ok {
		return
	}
	trailing = append(trailing, sqlRecoveredNullNode(root.ownerArena, trailing[len(trailing)-1], nullParentSym, nullLeafSym))

	bodyChildren := append(append([]*Node(nil), body.children...), trailing...)
	replaceNodeChildrenUnfielded(body, cloneNodeSliceIfArena(body.ownerArena, bodyChildren))
	body.setHasError(true)
	populateParentNode(selectClause, selectClause.children)
	selectClause.setHasError(true)
	populateParentNode(stmt, stmt.children)
	stmt.setHasError(true)
	replaceNodeChildrenUnfielded(root, cloneNodeSliceIfArena(root.ownerArena, []*Node{stmt}))
	root.setHasError(true)
}

func sqlSelectStatementBody(stmt *Node, lang *Language) (*Node, *Node) {
	if stmt == nil || len(stmt.children) == 0 {
		return nil, nil
	}
	selectClause := stmt.children[0]
	if selectClause == nil || selectClause.Type(lang) != "select_clause" || len(selectClause.children) < 2 {
		return nil, nil
	}
	body := selectClause.children[1]
	if body == nil || body.Type(lang) != "select_clause_body" {
		return nil, nil
	}
	return body, selectClause
}

func sqlSingleErrorComma(node *Node, lang *Language) *Node {
	if node == nil || node.Type(lang) != "ERROR" || len(node.children) != 1 {
		return nil
	}
	comma := node.children[0]
	if comma == nil || comma.Type(lang) != "," {
		return nil
	}
	return comma
}

func normalizeSQLRecoveredTopLevelSelectStatements(root *Node, source []byte, p *Parser, lang *Language) {
	if root == nil || p == nil || lang == nil || lang.Name != "sql" || root.Type(lang) != "source_file" || len(source) == 0 || root.ownerArena == nil || !root.HasError() {
		return
	}
	children := make([]*Node, 0, resultChildCount(root))
	for i := 0; i < resultChildCount(root); i++ {
		children = append(children, resultChildAt(root, i))
	}
	if len(children) == 0 {
		return
	}
	arena := root.ownerArena
	rebuilt := make([]*Node, 0, len(children))
	changed := false
	cursor := root.startByte

	for i := 0; i < len(children); {
		child := children[i]
		if child == nil {
			i++
			continue
		}

		if child.startByte > cursor {
			gapStart := sqlFirstNonSpace(source, cursor, child.startByte)
			if gapStart < child.startByte && sqlSourceHasKeywordAt(source, gapStart, "SELECT") {
				if stmt, end, hasSemi, ok := sqlRecoverDirectSelectGapStatement(source, gapStart, root.endByte, p, arena, lang); ok {
					skipEnd := end
					if hasSemi {
						if semiNode, semiOK := sqlBuildLeafNode(source, lang, arena, ";", false, end, end+1); semiOK {
							rebuilt = append(rebuilt, stmt, semiNode)
							skipEnd = end + 1
							changed = true
						} else {
							ok = false
						}
					} else {
						rebuilt = append(rebuilt, stmt)
						changed = true
					}
					if ok {
						i = sqlSkipChildrenBefore(children, i, skipEnd)
						cursor = skipEnd
						continue
					}
				}
			}
		}

		if sqlSourceStartsSelectTailKeyword(source, child.startByte) {
			if stmt, end, ok := sqlRecoverDirectSelectTailStatement(source, child.startByte, root.endByte, p, arena, lang); ok {
				rebuilt = append(rebuilt, stmt)
				i = sqlSkipChildrenBefore(children, i, end)
				cursor = end
				changed = true
				continue
			}
		}

		if replacement, ok := sqlRecoverSemicolonPrefixedSelectContinuation(child, source, p, lang, arena); ok {
			rebuilt = append(rebuilt, replacement...)
			cursor = child.endByte
			i++
			changed = true
			continue
		}

		rebuilt = append(rebuilt, child)
		if child.endByte > cursor {
			cursor = child.endByte
		}
		i++
	}

	if !changed {
		return
	}
	replaceNodeChildrenUnfielded(root, cloneNodeSliceIfArena(arena, rebuilt))
	rootHasError := false
	for _, child := range root.children {
		if child != nil && child.HasError() {
			rootHasError = true
			break
		}
	}
	root.setHasError(rootHasError)
}

func sqlRecoverDirectSelectGapStatement(source []byte, start, limit uint32, p *Parser, arena *nodeArena, lang *Language) (*Node, uint32, bool, bool) {
	if semi, ok := sqlFindByte(source, start, limit, ';'); ok {
		if stmt, ok := sqlRecoverDirectSelectStatementFromRange(source, start, semi, p, arena, lang); ok {
			return stmt, semi, true, true
		}
		limit = semi
	}
	for _, end := range sqlStatementCommentBoundaryEnds(source, start, limit) {
		if stmt, ok := sqlRecoverDirectSelectStatementFromRange(source, start, end, p, arena, lang); ok {
			return stmt, end, false, true
		}
	}
	return nil, 0, false, false
}

func sqlRecoverDirectSelectTailStatement(source []byte, tailStart, limit uint32, p *Parser, arena *nodeArena, lang *Language) (*Node, uint32, bool) {
	if !sqlSourceStartsSelectTailKeyword(source, tailStart) {
		return nil, 0, false
	}
	if semi, ok := sqlFindByte(source, tailStart, limit, ';'); ok {
		limit = semi
	}
	statementStart := tailStart
	if tailStart > 0 && sqlIsWhitespace(source[tailStart-1]) {
		statementStart = tailStart - 1
	}
	for _, end := range sqlStatementCommentBoundaryEnds(source, tailStart, limit) {
		if stmt, ok := sqlRecoverSelectContinuationFromRange(source, statementStart, tailStart, end, p, arena, lang); ok {
			return stmt, end, true
		}
	}
	if stmt, ok := sqlRecoverSelectContinuationFromRange(source, statementStart, tailStart, limit, p, arena, lang); ok {
		return stmt, limit, true
	}
	return nil, 0, false
}

func sqlRecoverDirectSelectStatementFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena, lang *Language) (*Node, bool) {
	if p == nil || lang == nil || arena == nil || start >= end || int(end) > len(source) || !sqlSourceHasKeywordAt(source, start, "SELECT") {
		return nil, false
	}
	tree, err := p.parseForRecovery(source[start:end])
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	parsedRoot := tree.RootNode()
	if parsedRoot == nil || parsedRoot.HasError() {
		tree.Release()
		return nil, false
	}
	stmt := sqlFirstChildOfType(parsedRoot, lang, "select_statement")
	if stmt == nil || stmt.startByte != 0 || stmt.endByte != end-start {
		tree.Release()
		return nil, false
	}
	offset := &cloneOffset{
		byteDelta: start,
		point:     advancePointByBytes(Point{}, source[:start]),
		baseRow:   parsedRoot.startPoint.Row,
	}
	clone := cloneTreeNodesIntoArenaWithOffset(stmt, arena, offset)
	tree.Release()
	return clone, clone != nil
}

func sqlRecoverSemicolonPrefixedSelectContinuation(node *Node, source []byte, p *Parser, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if node == nil || p == nil || lang == nil || arena == nil || node.Type(lang) != "ERROR" || node.startByte >= node.endByte || int(node.endByte) > len(source) {
		return nil, false
	}
	if source[node.startByte] != ';' {
		return nil, false
	}
	trailingSemi, ok := sqlFindLastByte(source, node.startByte+1, node.endByte, ';')
	if !ok || trailingSemi <= node.startByte {
		return nil, false
	}
	tailStart := sqlSkipSQLWhitespaceAndLineComments(source, node.startByte+1, trailingSemi)
	if tailStart >= trailingSemi || !sqlSourceStartsSelectTailKeyword(source, tailStart) {
		return nil, false
	}
	statementStart := tailStart
	if tailStart > node.startByte+1 && sqlIsWhitespace(source[tailStart-1]) {
		statementStart = tailStart - 1
	}
	stmt, ok := sqlRecoverSelectContinuationFromRange(source, statementStart, tailStart, trailingSemi, p, arena, lang)
	if !ok {
		return nil, false
	}
	leadingSemi, ok := sqlBuildLeafNode(source, lang, arena, ";", false, node.startByte, node.startByte+1)
	if !ok {
		return nil, false
	}
	trailingSemiNode, ok := sqlBuildLeafNode(source, lang, arena, ";", false, trailingSemi, trailingSemi+1)
	if !ok {
		return nil, false
	}
	out := []*Node{leadingSemi}
	sqlAppendClonedCommentsInRange(node, source, lang, arena, node.startByte+1, statementStart, &out)
	out = append(out, stmt, trailingSemiNode)
	return out, true
}

func sqlRecoverSelectContinuationFromRange(source []byte, statementStart, tailStart, end uint32, p *Parser, arena *nodeArena, lang *Language) (*Node, bool) {
	if p == nil || lang == nil || arena == nil || statementStart > tailStart || tailStart >= end || int(end) > len(source) {
		return nil, false
	}
	const prefix = "SELECT *\n"
	if tailStart < uint32(len(prefix)) {
		return nil, false
	}
	wrapped := make([]byte, 0, len(prefix)+int(end-tailStart))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[tailStart:end]...)
	tree, err := p.parseForRecovery(wrapped)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	parsedRoot := tree.RootNode()
	startPoint := advancePointByBytes(Point{}, source[:tailStart])
	prefixPoint := advancePointByBytes(Point{}, []byte(prefix))
	if startPoint.Row < prefixPoint.Row {
		tree.Release()
		return nil, false
	}
	if parsedRoot == nil || parsedRoot.HasError() {
		tree.Release()
		return nil, false
	}
	wrappedStmt := sqlFirstChildOfType(parsedRoot, lang, "select_statement")
	if wrappedStmt == nil || resultChildCount(wrappedStmt) < 2 {
		tree.Release()
		return nil, false
	}
	firstChild := resultChildAt(wrappedStmt, 0)
	if firstChild == nil || firstChild.Type(lang) != "select_clause" {
		tree.Release()
		return nil, false
	}

	missingSelect, ok := sqlBuildMissingSelectClause(source, statementStart, lang, arena)
	if !ok {
		tree.Release()
		return nil, false
	}
	stmtChildren := make([]*Node, 0, resultChildCount(wrappedStmt))
	stmtChildren = append(stmtChildren, missingSelect)
	offset := &cloneOffset{
		byteDelta: tailStart - uint32(len(prefix)),
		point:     Point{Row: startPoint.Row - prefixPoint.Row, Column: startPoint.Column},
		baseRow:   parsedRoot.startPoint.Row,
	}
	for i := 1; i < resultChildCount(wrappedStmt); i++ {
		child := resultChildAt(wrappedStmt, i)
		if child == nil {
			continue
		}
		stmtChildren = append(stmtChildren, cloneTreeNodesIntoArenaWithOffset(child, arena, offset))
	}
	tree.Release()
	if len(stmtChildren) == 1 {
		return nil, false
	}
	stmtSym, ok := symbolByName(lang, "select_statement")
	if !ok {
		return nil, false
	}
	stmt := newParentNodeInArena(arena, stmtSym, symbolIsNamed(lang, stmtSym), cloneNodeSliceIfArena(arena, stmtChildren), nil, 0)
	stmt.setHasError(true)
	return stmt, true
}

func sqlBuildMissingSelectClause(source []byte, pos uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	selectClauseSym, ok := symbolByName(lang, "select_clause")
	if !ok {
		return nil, false
	}
	selectSym, ok := findVisibleSymbolByName(lang, "SELECT", false)
	if !ok {
		return nil, false
	}
	point := advancePointByBytes(Point{}, source[:pos])
	leaf := newLeafNodeInArena(arena, selectSym, false, pos, pos, point, point)
	leaf.setMissing(true)
	leaf.setHasError(true)
	clause := newParentNodeInArena(arena, selectClauseSym, symbolIsNamed(lang, selectClauseSym), []*Node{leaf}, nil, 0)
	clause.setHasError(true)
	return clause, true
}

func sqlBuildLeafNode(source []byte, lang *Language, arena *nodeArena, name string, named bool, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start > end || int(end) > len(source) {
		return nil, false
	}
	sym, ok := findVisibleSymbolByName(lang, name, named)
	if !ok {
		return nil, false
	}
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	return newLeafNodeInArena(arena, sym, named, start, end, startPoint, endPoint), true
}

func sqlAppendClonedCommentsInRange(node *Node, source []byte, lang *Language, arena *nodeArena, start, end uint32, out *[]*Node) {
	if node == nil || lang == nil || arena == nil || out == nil || start > end || int(end) > len(source) {
		return
	}
	if node.Type(lang) == "comment" && node.startByte >= start && node.endByte <= end {
		*out = append(*out, cloneTreeNodesIntoArena(node, arena))
		return
	}
	for i := 0; i < resultChildCount(node); i++ {
		sqlAppendClonedCommentsInRange(resultChildAt(node, i), source, lang, arena, start, end, out)
	}
}

func sqlFirstChildOfType(node *Node, lang *Language, typ string) *Node {
	if node == nil || lang == nil {
		return nil
	}
	for i := 0; i < resultChildCount(node); i++ {
		child := resultChildAt(node, i)
		if child != nil && child.Type(lang) == typ {
			return child
		}
	}
	return nil
}

func sqlSkipChildrenBefore(children []*Node, startIdx int, byteEnd uint32) int {
	i := startIdx
	for i < len(children) {
		child := children[i]
		if child != nil && child.startByte >= byteEnd {
			break
		}
		i++
	}
	return i
}

func sqlFirstNonSpace(source []byte, start, end uint32) uint32 {
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	for start < end && sqlIsWhitespace(source[start]) {
		start++
	}
	return start
}

func sqlSkipSQLWhitespaceAndLineComments(source []byte, start, end uint32) uint32 {
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	pos := start
	for pos < end {
		for pos < end && sqlIsWhitespace(source[pos]) {
			pos++
		}
		if pos+1 >= end || source[pos] != '-' || source[pos+1] != '-' {
			break
		}
		pos += 2
		for pos < end && source[pos] != '\n' {
			pos++
		}
	}
	return pos
}

func sqlFindByte(source []byte, start, end uint32, b byte) (uint32, bool) {
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	for i := start; i < end; i++ {
		if source[i] == b {
			return i, true
		}
	}
	return 0, false
}

func sqlFindLastByte(source []byte, start, end uint32, b byte) (uint32, bool) {
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	found := uint32(0)
	ok := false
	for i := start; i < end; i++ {
		if source[i] == b {
			found = i
			ok = true
		}
	}
	return found, ok
}

func sqlStatementCommentBoundaryEnds(source []byte, start, limit uint32) []uint32 {
	if int(limit) > len(source) {
		limit = uint32(len(source))
	}
	var out []uint32
	for pos := start; pos+1 < limit; pos++ {
		if source[pos] != '-' || source[pos+1] != '-' {
			continue
		}
		end := sqlTrimRightWhitespaceEnd(source, start, pos)
		if end > start {
			out = append(out, end)
		}
		for pos < limit && source[pos] != '\n' {
			pos++
		}
	}
	return out
}

func sqlTrimRightWhitespaceEnd(source []byte, start, end uint32) uint32 {
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	for end > start && sqlIsWhitespace(source[end-1]) {
		end--
	}
	return end
}

func sqlSourceStartsSelectTailKeyword(source []byte, pos uint32) bool {
	return sqlSourceHasKeywordAt(source, pos, "FROM") ||
		sqlSourceHasKeywordAt(source, pos, "WHERE") ||
		sqlSourceHasKeywordAt(source, pos, "GROUP") ||
		sqlSourceHasKeywordAt(source, pos, "ORDER")
}

func sqlSourceHasKeywordAt(source []byte, pos uint32, keyword string) bool {
	if int(pos) >= len(source) || int(pos)+len(keyword) > len(source) {
		return false
	}
	if pos > 0 && sqlIsIdentByte(source[pos-1]) {
		return false
	}
	for i := 0; i < len(keyword); i++ {
		if sqlUpperASCII(source[int(pos)+i]) != keyword[i] {
			return false
		}
	}
	after := int(pos) + len(keyword)
	return after >= len(source) || !sqlIsIdentByte(source[after])
}

func sqlUpperASCII(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}

func sqlIsWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func sqlIsIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
