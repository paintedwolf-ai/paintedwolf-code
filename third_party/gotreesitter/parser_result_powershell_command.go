package gotreesitter

import "bytes"

func powerShellReuseExactNode(nodes []*Node, lang *Language, typ string, start, end uint32) *Node {
	for _, node := range nodes {
		if node == nil || node.Type(lang) != typ {
			continue
		}
		if node.startByte == start && node.endByte == end {
			return node
		}
	}
	return nil
}

func powerShellKeywordAt(source []byte, pos int, kw string) bool {
	if pos < 0 || pos+len(kw) > len(source) || !bytes.HasPrefix(source[pos:], []byte(kw)) {
		return false
	}
	if pos > 0 {
		if prev := source[pos-1]; (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || prev == '_' {
			return false
		}
	}
	if pos+len(kw) < len(source) {
		if next := source[pos+len(kw)]; (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || next == '_' {
			return false
		}
	}
	return true
}

func powerShellSkipTrivia(source []byte, start, end int) int {
	for start < end {
		switch source[start] {
		case ' ', '\t', '\r', '\n':
			start++
		default:
			return start
		}
	}
	return start
}

func powerShellSkipInlineSpace(source []byte, start, end int) int {
	for start < end && (source[start] == ' ' || source[start] == '\t') {
		start++
	}
	return start
}

func powerShellTrimInlineSpace(source []byte, start, end int) (int, int) {
	start = powerShellSkipInlineSpace(source, start, end)
	return start, powerShellTrimInlineSpaceRight(source, start, end)
}

func powerShellTrimInlineSpaceRight(source []byte, start, end int) int {
	for end > start && (source[end-1] == ' ' || source[end-1] == '\t') {
		end--
	}
	return end
}

func powerShellLineEnd(source []byte, start, end int) int {
	for start < end && source[start] != '\n' {
		start++
	}
	return start
}

func powerShellFindAssignmentByte(source []byte, start, end int) int {
	inString := false
	depthParen := 0
	depthBracket := 0
	for i := start; i < end; i++ {
		switch source[i] {
		case '"':
			if !isEscapedQuote(source, uint32(i)) {
				inString = !inString
			}
		case '(':
			if !inString {
				depthParen++
			}
		case ')':
			if !inString && depthParen > 0 {
				depthParen--
			}
		case '[':
			if !inString {
				depthBracket++
			}
		case ']':
			if !inString && depthBracket > 0 {
				depthBracket--
			}
		case '=':
			if !inString && depthParen == 0 && depthBracket == 0 {
				return i
			}
		}
	}
	return -1
}

func powerShellFindTopLevelPlus(source []byte, start, end int) int {
	inString := false
	depthParen := 0
	depthBracket := 0
	for i := start; i < end; i++ {
		switch source[i] {
		case '"':
			if !isEscapedQuote(source, uint32(i)) {
				inString = !inString
			}
		case '(':
			if !inString {
				depthParen++
			}
		case ')':
			if !inString && depthParen > 0 {
				depthParen--
			}
		case '[':
			if !inString {
				depthBracket++
			}
		case ']':
			if !inString && depthBracket > 0 {
				depthBracket--
			}
		case '+':
			if !inString && depthParen == 0 && depthBracket == 0 {
				return i
			}
		}
	}
	return -1
}

func powerShellLooksLikeCommandText(source []byte, start, end int) bool {
	start, end = powerShellTrimInlineSpace(source, start, end)
	if start >= end {
		return false
	}
	switch source[start] {
	case '$', '"', '!', '(':
		return false
	}
	if !((source[start] >= 'a' && source[start] <= 'z') || (source[start] >= 'A' && source[start] <= 'Z') || source[start] == '_') {
		return false
	}
	return bytes.ContainsAny(source[start:end], " \t")
}

func findMatchingDelimitedByte(source []byte, openPos, limit int, open, close byte) int {
	if openPos < 0 || openPos >= len(source) {
		return -1
	}
	if limit > len(source) {
		limit = len(source)
	}
	depth := 0
	inString := false
	for i := openPos; i < limit; i++ {
		switch source[i] {
		case '"':
			if !isEscapedQuote(source, uint32(i)) {
				inString = !inString
			}
		default:
			if inString {
				continue
			}
			if source[i] == open {
				depth++
			} else if source[i] == close {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func buildPowerShellTrailingPipelines(arena *nodeArena, source []byte, lang *Language, start, end uint32, pipelineSym, pipelineChainSym, commandSym, commandNameSym, commandElementsSym, commandArgSepSym, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym, spaceSym Symbol) []*Node {
	out := make([]*Node, 0, 4)
	i := int(start)
	limit := int(end)
	for i < limit {
		for i < limit && (source[i] == ' ' || source[i] == '\t' || source[i] == '\r' || source[i] == '\n') {
			i++
		}
		if i >= limit {
			break
		}
		lineStart := i
		for i < limit && source[i] != '\n' {
			i++
		}
		lineEnd := i
		if pipeline := buildPowerShellPipelineFromLine(arena, source, lang, lineStart, lineEnd, pipelineSym, pipelineChainSym, commandSym, commandNameSym, commandElementsSym, commandArgSepSym, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym, spaceSym); pipeline != nil {
			out = append(out, pipeline)
		}
	}
	if arena != nil && len(out) > 0 {
		buf := arena.allocNodeSlice(len(out))
		copy(buf, out)
		out = buf
	}
	return out
}

func buildPowerShellPipelineFromLine(arena *nodeArena, source []byte, lang *Language, start, end int, pipelineSym, pipelineChainSym, commandSym, commandNameSym, commandElementsSym, commandArgSepSym, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym, spaceSym Symbol) *Node {
	if start >= end {
		return nil
	}
	commandNameEnd := start
	for commandNameEnd < end && source[commandNameEnd] != ' ' && source[commandNameEnd] != '\t' {
		commandNameEnd++
	}
	if commandNameEnd == start {
		return nil
	}
	commandNameStartPoint := advancePointByBytes(Point{}, source[:start])
	commandNameEndPoint := advancePointByBytes(commandNameStartPoint, source[start:commandNameEnd])
	commandNameNamed := symbolIsNamed(lang, commandNameSym)
	commandName := newLeafNodeInArena(arena, commandNameSym, commandNameNamed, uint32(start), uint32(commandNameEnd), commandNameStartPoint, commandNameEndPoint)

	commandChildren := []*Node{commandName}
	elements := buildPowerShellCommandElements(arena, source, lang, commandNameEnd, end, commandElementsSym, commandArgSepSym, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym, spaceSym)
	if elements != nil {
		commandChildren = append(commandChildren, elements)
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(commandChildren))
		copy(buf, commandChildren)
		commandChildren = buf
	}
	commandNamed := symbolIsNamed(lang, commandSym)
	command := newParentNodeInArena(arena, commandSym, commandNamed, commandChildren, nil, 0)
	command.endByte = uint32(end)
	command.endPoint = advancePointByBytes(command.startPoint, source[start:end])
	for fieldIdx, fieldName := range lang.FieldNames {
		switch fieldName {
		case "command_name":
			ensureNodeFieldStorage(command, len(command.children))
			command.fieldIDs()[0] = FieldID(fieldIdx)
			command.fieldSources()[0] = fieldSourceDirect
		case "command_elements":
			if len(command.children) > 1 {
				ensureNodeFieldStorage(command, len(command.children))
				command.fieldIDs()[1] = FieldID(fieldIdx)
				command.fieldSources()[1] = fieldSourceDirect
			}
		}
	}

	chainChildren := []*Node{command}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = command
		chainChildren = buf
	}
	pipelineChainNamed := symbolIsNamed(lang, pipelineChainSym)
	chain := newParentNodeInArena(arena, pipelineChainSym, pipelineChainNamed, chainChildren, nil, 0)
	pipelineChildren := []*Node{chain}
	if arena != nil {
		buf := arena.allocNodeSlice(1)
		buf[0] = chain
		pipelineChildren = buf
	}
	pipelineNamed := symbolIsNamed(lang, pipelineSym)
	return newParentNodeInArena(arena, pipelineSym, pipelineNamed, pipelineChildren, nil, 0)
}

func buildPowerShellCommandElements(arena *nodeArena, source []byte, lang *Language, start, end int, commandElementsSym, commandArgSepSym, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym, spaceSym Symbol) *Node {
	children := make([]*Node, 0, 8)
	i := start
	for i < end {
		sepStart := i
		for i < end && (source[i] == ' ' || source[i] == '\t') {
			i++
		}
		if i == sepStart {
			break
		}
		sepLeafStart := advancePointByBytes(Point{}, source[:sepStart])
		sepLeafEnd := advancePointByBytes(sepLeafStart, source[sepStart:i])
		spaceLeaf := newLeafNodeInArena(arena, spaceSym, false, uint32(sepStart), uint32(i), sepLeafStart, sepLeafEnd)
		sepChildren := []*Node{spaceLeaf}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = spaceLeaf
			sepChildren = buf
		}
		sepNamed := symbolIsNamed(lang, commandArgSepSym)
		sep := newParentNodeInArena(arena, commandArgSepSym, sepNamed, sepChildren, nil, 0)
		children = append(children, sep)

		tokenStart := i
		tokenEnd := powerShellTokenEnd(source, i, end)
		if tokenEnd <= tokenStart {
			break
		}
		children = append(children, buildPowerShellCommandElement(arena, source, lang, tokenStart, tokenEnd, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym))
		i = tokenEnd
	}
	if len(children) == 0 {
		return nil
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	elementsNamed := symbolIsNamed(lang, commandElementsSym)
	return newParentNodeInArena(arena, commandElementsSym, elementsNamed, children, nil, 0)
}

func buildPowerShellCommandElement(arena *nodeArena, source []byte, lang *Language, start, end int, commandParameterSym, arrayLiteralSym, unaryExprSym, variableSym, stringLiteralSym, expandableStringSym, genericTokenSym Symbol) *Node {
	startPoint := advancePointByBytes(Point{}, source[:start])
	endPoint := advancePointByBytes(startPoint, source[start:end])
	if source[start] == '-' {
		named := symbolIsNamed(lang, commandParameterSym)
		return newLeafNodeInArena(arena, commandParameterSym, named, uint32(start), uint32(end), startPoint, endPoint)
	}
	if source[start] == '$' {
		variable := buildPowerShellVariableMemberAccess(arena, source, lang, start, end)
		if variable == nil {
			variableNamed := symbolIsNamed(lang, variableSym)
			variable = newLeafNodeInArena(arena, variableSym, variableNamed, uint32(start), uint32(end), startPoint, endPoint)
		}
		unaryChildren := []*Node{variable}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = variable
			unaryChildren = buf
		}
		unaryNamed := symbolIsNamed(lang, unaryExprSym)
		unary := newParentNodeInArena(arena, unaryExprSym, unaryNamed, unaryChildren, nil, 0)
		arrayChildren := []*Node{unary}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = unary
			arrayChildren = buf
		}
		arrayNamed := symbolIsNamed(lang, arrayLiteralSym)
		return newParentNodeInArena(arena, arrayLiteralSym, arrayNamed, arrayChildren, nil, 0)
	}
	if source[start] == '(' && source[end-1] == ')' {
		parenthesized := buildPowerShellParenthesizedExpression(arena, source, lang, start, end)
		if parenthesized != nil {
			unaryChildren := []*Node{parenthesized}
			if arena != nil {
				buf := arena.allocNodeSlice(1)
				buf[0] = parenthesized
				unaryChildren = buf
			}
			unaryNamed := symbolIsNamed(lang, unaryExprSym)
			unary := newParentNodeInArena(arena, unaryExprSym, unaryNamed, unaryChildren, nil, 0)
			arrayChildren := []*Node{unary}
			if arena != nil {
				buf := arena.allocNodeSlice(1)
				buf[0] = unary
				arrayChildren = buf
			}
			arrayNamed := symbolIsNamed(lang, arrayLiteralSym)
			return newParentNodeInArena(arena, arrayLiteralSym, arrayNamed, arrayChildren, nil, 0)
		}
	}
	if source[start] == '"' && source[end-1] == '"' {
		expandable := buildPowerShellExpandableStringLiteralFromSymbol(arena, source, lang, start, end, expandableStringSym)
		if expandable == nil {
			return nil
		}
		stringChildren := []*Node{expandable}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = expandable
			stringChildren = buf
		}
		stringNamed := symbolIsNamed(lang, stringLiteralSym)
		stringNode := newParentNodeInArena(arena, stringLiteralSym, stringNamed, stringChildren, nil, 0)
		unaryChildren := []*Node{stringNode}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = stringNode
			unaryChildren = buf
		}
		unaryNamed := symbolIsNamed(lang, unaryExprSym)
		unary := newParentNodeInArena(arena, unaryExprSym, unaryNamed, unaryChildren, nil, 0)
		arrayChildren := []*Node{unary}
		if arena != nil {
			buf := arena.allocNodeSlice(1)
			buf[0] = unary
			arrayChildren = buf
		}
		arrayNamed := symbolIsNamed(lang, arrayLiteralSym)
		return newParentNodeInArena(arena, arrayLiteralSym, arrayNamed, arrayChildren, nil, 0)
	}
	genericNamed := symbolIsNamed(lang, genericTokenSym)
	return newLeafNodeInArena(arena, genericTokenSym, genericNamed, uint32(start), uint32(end), startPoint, endPoint)
}

func buildPowerShellVariableMemberAccess(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	memberAccessSym, memberAccessNamed, ok := symbolMeta(lang, "member_access")
	if !ok {
		return nil
	}
	variableSym, variableNamed, ok := symbolMeta(lang, "variable")
	if !ok {
		return nil
	}
	backslashSym, backslashNamed, ok := symbolMeta(lang, "\\")
	if !ok {
		return nil
	}
	dotSym, dotNamed, ok := symbolMeta(lang, ".")
	if !ok {
		return nil
	}
	varEnd := start + 1
	for varEnd < end {
		b := source[varEnd]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			varEnd++
			continue
		}
		break
	}
	if varEnd >= end || source[varEnd] != '\\' {
		return nil
	}
	dot := bytes.LastIndexByte(source[varEnd:end], '.')
	if dot < 0 {
		return nil
	}
	dot += varEnd
	variable := newLeafNodeInArena(arena, variableSym, variableNamed, uint32(start), uint32(varEnd), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:varEnd]))
	pathName := buildPowerShellSimpleName(arena, source, lang, varEnd+1, dot)
	memberName := buildPowerShellMemberName(arena, source, lang, dot+1, end)
	if pathName == nil || memberName == nil {
		return nil
	}
	errChildren := []*Node{
		newLeafNodeInArena(arena, backslashSym, backslashNamed, uint32(varEnd), uint32(varEnd+1), advancePointByBytes(Point{}, source[:varEnd]), advancePointByBytes(advancePointByBytes(Point{}, source[:varEnd]), source[varEnd:varEnd+1])),
		pathName,
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(errChildren))
		copy(buf, errChildren)
		errChildren = buf
	}
	errNode := newParentNodeInArena(arena, errorSymbol, true, errChildren, nil, 0)
	errNode.setHasError(true)
	errNode.setExtra(true)
	children := []*Node{
		variable,
		errNode,
		newLeafNodeInArena(arena, dotSym, dotNamed, uint32(dot), uint32(dot+1), advancePointByBytes(Point{}, source[:dot]), advancePointByBytes(advancePointByBytes(Point{}, source[:dot]), source[dot:dot+1])),
		memberName,
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	return newParentNodeInArena(arena, memberAccessSym, memberAccessNamed, children, nil, 0)
}

func buildPowerShellExpandableStringLiteral(arena *nodeArena, source []byte, lang *Language, start, end int) *Node {
	expandableSym, _, ok := symbolMeta(lang, "expandable_string_literal")
	if !ok {
		return nil
	}
	return buildPowerShellExpandableStringLiteralFromSymbol(arena, source, lang, start, end, expandableSym)
}

func buildPowerShellExpandableStringLiteralFromSymbol(arena *nodeArena, source []byte, lang *Language, start, end int, expandableSym Symbol) *Node {
	if start >= end {
		return nil
	}
	expandableNamed := symbolIsNamed(lang, expandableSym)
	variableSym, variableNamed, ok := symbolMeta(lang, "variable")
	if !ok {
		return newLeafNodeInArena(arena, expandableSym, expandableNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	}
	var children []*Node
	for i := start + 1; i < end-1; i++ {
		if source[i] != '$' {
			continue
		}
		j := i + 1
		for j < end-1 {
			b := source[j]
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
				j++
				continue
			}
			break
		}
		if j == i+1 {
			continue
		}
		children = append(children, newLeafNodeInArena(arena, variableSym, variableNamed, uint32(i), uint32(j), advancePointByBytes(Point{}, source[:i]), advancePointByBytes(advancePointByBytes(Point{}, source[:i]), source[i:j])))
		i = j - 1
	}
	if len(children) == 0 {
		return newLeafNodeInArena(arena, expandableSym, expandableNamed, uint32(start), uint32(end), advancePointByBytes(Point{}, source[:start]), advancePointByBytes(advancePointByBytes(Point{}, source[:start]), source[start:end]))
	}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	node := newParentNodeInArena(arena, expandableSym, expandableNamed, children, nil, 0)
	node.startByte = uint32(start)
	node.endByte = uint32(end)
	node.startPoint = advancePointByBytes(Point{}, source[:start])
	node.endPoint = advancePointByBytes(node.startPoint, source[start:end])
	return node
}

func powerShellTokenEnd(source []byte, start, end int) int {
	if start >= end {
		return start
	}
	if source[start] == '"' {
		for i := start + 1; i < end; i++ {
			if source[i] == '"' && !isEscapedQuote(source, uint32(i)) {
				return i + 1
			}
		}
		return end
	}
	if source[start] == '(' {
		if close := findMatchingDelimitedByte(source, start, end, '(', ')'); close >= 0 {
			return close + 1
		}
		return end
	}
	i := start
	for i < end && source[i] != ' ' && source[i] != '\t' {
		i++
	}
	return i
}

func findMatchingBraceByte(source []byte, openPos, limit int) int {
	if openPos < 0 || openPos >= len(source) {
		return -1
	}
	if limit > len(source) {
		limit = len(source)
	}
	depth := 0
	for i := openPos; i < limit; i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
