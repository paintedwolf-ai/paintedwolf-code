package gotreesitter

func csharpFindTopLevelOperator(source []byte, start, end uint32, op string) (uint32, bool) {
	if start >= end || op == "" {
		return 0, false
	}
	parens := 0
	braces := 0
	brackets := 0
	inString := false
	escape := false
	opLen := uint32(len(op))
	for i := start; i+opLen <= end; i++ {
		b := source[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			continue
		case '(':
			parens++
			continue
		case ')':
			if parens > 0 {
				parens--
			}
			continue
		case '{':
			braces++
			continue
		case '}':
			if braces > 0 {
				braces--
			}
			continue
		case '[':
			brackets++
			continue
		case ']':
			if brackets > 0 {
				brackets--
			}
			continue
		}
		if parens == 0 && braces == 0 && brackets == 0 && string(source[i:i+opLen]) == op {
			return i, true
		}
	}
	return 0, false
}

func csharpFindLastTopLevelOperator(source []byte, start, end uint32, op string) (uint32, bool) {
	if start >= end || op == "" {
		return 0, false
	}
	parens := 0
	braces := 0
	brackets := 0
	inString := false
	escape := false
	opLen := uint32(len(op))
	last := uint32(0)
	found := false
	for i := start; i+opLen <= end; i++ {
		b := source[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			continue
		case '(':
			parens++
			continue
		case ')':
			if parens > 0 {
				parens--
			}
			continue
		case '{':
			braces++
			continue
		case '}':
			if braces > 0 {
				braces--
			}
			continue
		case '[':
			brackets++
			continue
		case ']':
			if brackets > 0 {
				brackets--
			}
			continue
		}
		if parens == 0 && braces == 0 && brackets == 0 && string(source[i:i+opLen]) == op {
			last = i
			found = true
		}
	}
	return last, found
}

func csharpFindTopLevelKeyword(source []byte, start, end uint32, kw string) (uint32, bool) {
	if start >= end || kw == "" {
		return 0, false
	}
	parens := 0
	braces := 0
	brackets := 0
	inString := false
	escape := false
	kwLen := uint32(len(kw))
	for i := start; i+kwLen <= end; i++ {
		b := source[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			continue
		case '(':
			parens++
			continue
		case ')':
			if parens > 0 {
				parens--
			}
			continue
		case '{':
			braces++
			continue
		case '}':
			if braces > 0 {
				braces--
			}
			continue
		case '[':
			brackets++
			continue
		case ']':
			if brackets > 0 {
				brackets--
			}
			continue
		}
		if parens != 0 || braces != 0 || brackets != 0 || string(source[i:i+kwLen]) != kw {
			continue
		}
		if i > start && csharpIdentifierContinueByte(source[i-1]) {
			continue
		}
		if i+kwLen < end && csharpIdentifierContinueByte(source[i+kwLen]) {
			continue
		}
		return i, true
	}
	return 0, false
}

func csharpFindConditionalColon(source []byte, start, end uint32) (uint32, bool) {
	parens := 0
	braces := 0
	brackets := 0
	inString := false
	escape := false
	nestedTernary := 0
	for i := start; i < end; i++ {
		b := source[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '(':
			parens++
		case ')':
			if parens > 0 {
				parens--
			}
		case '{':
			braces++
		case '}':
			if braces > 0 {
				braces--
			}
		case '[':
			brackets++
		case ']':
			if brackets > 0 {
				brackets--
			}
		case '?':
			if parens == 0 && braces == 0 && brackets == 0 {
				nestedTernary++
			}
		case ':':
			if parens == 0 && braces == 0 && brackets == 0 {
				if nestedTernary == 0 {
					return i, true
				}
				nestedTernary--
			}
		}
	}
	return 0, false
}

func csharpFindTopLevelAssignment(source []byte, start, end uint32) (uint32, bool) {
	pos, ok := csharpFindTopLevelOperator(source, start, end, "=")
	if !ok {
		return 0, false
	}
	if pos > start && source[pos-1] == '=' {
		return 0, false
	}
	if pos+1 < end && (source[pos+1] == '=' || source[pos+1] == '>') {
		return 0, false
	}
	return pos, true
}

func csharpFindInvocationOpenParen(source []byte, start, end uint32) (uint32, bool) {
	if end <= start || source[end-1] != ')' {
		return 0, false
	}
	depth := 0
	inString := false
	escape := false
	for i := end; i > start; i-- {
		b := source[i-1]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return i - 1, true
			}
		}
	}
	return 0, false
}

func csharpSplitTopLevelByComma(source []byte, start, end uint32) [][2]uint32 {
	var spans [][2]uint32
	itemStart := start
	parens := 0
	braces := 0
	brackets := 0
	inString := false
	escape := false
	for i := start; i < end; i++ {
		b := source[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '(':
			parens++
		case ')':
			if parens > 0 {
				parens--
			}
		case '{':
			braces++
		case '}':
			if braces > 0 {
				braces--
			}
		case '[':
			brackets++
		case ']':
			if brackets > 0 {
				brackets--
			}
		case ',':
			if parens == 0 && braces == 0 && brackets == 0 {
				spans = append(spans, [2]uint32{itemStart, i})
				itemStart = i + 1
			}
		}
	}
	if itemStart <= end {
		spans = append(spans, [2]uint32{itemStart, end})
	}
	return spans
}

func csharpFindCommaBetween(source []byte, start, end uint32) uint32 {
	for i := start; i < end && i < uint32(len(source)); i++ {
		if source[i] == ',' {
			return i
		}
	}
	return 0
}

func csharpIsIntegerLiteral(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, ch := range b {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func csharpExtractRecoveredVariableInitializer(root *Node, lang *Language, arena *nodeArena) *Node {
	if root == nil || lang == nil {
		return nil
	}
	var walk func(*Node) *Node
	walk = func(n *Node) *Node {
		if n == nil {
			return nil
		}
		if n.Type(lang) == "variable_declarator" && len(n.children) >= 3 {
			value := n.children[len(n.children)-1]
			if value != nil {
				if arena != nil {
					return cloneTreeNodesIntoArena(value, arena)
				}
				return value
			}
		}
		for i := 0; i < n.ChildCount(); i++ {
			if got := walk(n.Child(i)); got != nil {
				return got
			}
		}
		return nil
	}
	return walk(root)
}

// csharpFindMatchingBraceByte finds the byte offset of the '}' that closes the
// '{' at openPos, scanning [openPos, limit) while skipping braces that appear
// inside line/block comments, regular and verbatim strings, and char literals.
// The plain findMatchingBraceByte miscounts braces embedded in C# char literals
// (e.g. '{' / '}') and string content, which truncates declaration spans on
// real-world files (issue #115). Returns the index of the matching '}', or -1.
func csharpFindMatchingBraceByte(source []byte, openPos, limit int) int {
	if openPos < 0 || openPos >= len(source) || source[openPos] != '{' {
		return -1
	}
	if limit > len(source) {
		limit = len(source)
	}
	depth := 0
	inLineComment := false
	inBlockComment := false
	inString := false
	inChar := false
	verbatimString := false
	escape := false
	for i := openPos; i < limit; i++ {
		b := source[i]
		switch {
		case inLineComment:
			if b == '\n' {
				inLineComment = false
			}
			continue
		case inBlockComment:
			if i > 0 && source[i-1] == '*' && b == '/' {
				inBlockComment = false
			}
			continue
		case inString:
			if verbatimString {
				if b == '"' {
					if i+1 < limit && source[i+1] == '"' {
						i++
						continue
					}
					inString = false
					verbatimString = false
				}
				continue
			}
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		case inChar:
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '\'' {
				inChar = false
			}
			continue
		}
		if b == '/' && i+1 < limit {
			switch source[i+1] {
			case '/':
				inLineComment = true
				i++
				continue
			case '*':
				inBlockComment = true
				i++
				continue
			}
		}
		switch b {
		case '"':
			inString = true
			// Verbatim strings (@"...") and verbatim interpolated strings, in
			// either order (@$"..." since C# 8, or $@"..."), terminate on a lone
			// '"' with "" as the escaped quote rather than using backslash
			// escapes. Detect '@' immediately before the quote, or '@$' before it.
			verbatimString = (i > 0 && source[i-1] == '@') ||
				(i > 1 && source[i-1] == '$' && source[i-2] == '@')
			escape = false
		case '\'':
			inChar = true
			escape = false
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

func csharpHasKeywordAt(source []byte, start uint32, kw string) bool {
	if int(start)+len(kw) > len(source) {
		return false
	}
	return string(source[start:uint32(int(start)+len(kw))]) == kw
}

func csharpSkipSpaceBytes(source []byte, start uint32) uint32 {
	i := start
	for i < uint32(len(source)) {
		switch source[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func csharpTrimRightSpaceBytes(source []byte, end uint32) uint32 {
	for end > 0 {
		switch source[end-1] {
		case ' ', '\t', '\n', '\r':
			end--
		default:
			return end
		}
	}
	return end
}

func csharpTrimSpaceBounds(source []byte, start, end uint32) (uint32, uint32) {
	start = csharpSkipSpaceBytes(source, start)
	end = csharpTrimRightSpaceBytes(source, end)
	if start > end {
		return end, end
	}
	return start, end
}

func csharpScanIdentifierAt(source []byte, start uint32) (uint32, uint32, bool) {
	if start >= uint32(len(source)) {
		return 0, 0, false
	}
	b := source[start]
	if !csharpIdentifierStartByte(b) {
		return 0, 0, false
	}
	end := start + 1
	for end < uint32(len(source)) && csharpIdentifierContinueByte(source[end]) {
		end++
	}
	return start, end, true
}

func csharpBuildIdentifierNodeFromSource(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	ident, ok := csharpBuildLeafNodeByName(arena, source, lang, "identifier", start, end)
	if !ok || lang == nil || int(end) > len(source) || start >= end {
		return ident, ok
	}
	keyword := string(source[start:end])
	keywordSym, ok := symbolByName(lang, keyword)
	if !ok || !symbolHasMetadata(lang, keywordSym) || symbolIsNamed(lang, keywordSym) {
		return ident, true
	}
	keywordLeaf, ok := csharpBuildLeafNodeByName(arena, source, lang, keyword, start, end)
	if !ok {
		return ident, true
	}
	identSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return ident, true
	}
	identNamed := symbolIsNamed(lang, identSym)
	children := []*Node{keywordLeaf}
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	node := newParentNodeInArena(arena, identSym, identNamed, children, nil, 0)
	node.setHasError(false)
	return node, true
}

func csharpIdentifierStartByte(b byte) bool {
	return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func csharpIdentifierContinueByte(b byte) bool {
	return csharpIdentifierStartByte(b) || b >= '0' && b <= '9'
}

// csharpFindTopLevelNamespaceKeyword scans [start, end) for the first
// occurrence of the "namespace" keyword that is not part of a longer
// identifier and not inside a comment, string, or char literal, skipping
// past preprocessor directive lines (#region/#endregion/#if/etc). It is a
// heuristic scan, not full lexing — SHORT-TERM RELIEF for issue #136, used
// only as a fallback in csharpRecoverNamespaceFromChildren when a file's
// leading boilerplate (copyright header comments, using directives) has
// collapsed into the same opaque ERROR span as the namespace keyword itself,
// so the keyword is not at that span's own start byte.
func csharpFindTopLevelNamespaceKeyword(source []byte, start, end uint32) (uint32, bool) {
	if end > uint32(len(source)) {
		end = uint32(len(source))
	}
	inLineComment := false
	inBlockComment := false
	inString := false
	inChar := false
	verbatimString := false
	escape := false
	for i := start; i < end; i++ {
		b := source[i]
		if inLineComment {
			if b == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if i > start && source[i-1] == '*' && b == '/' {
				inBlockComment = false
			}
			continue
		}
		if inString {
			if verbatimString {
				if b == '"' {
					if i+1 < end && source[i+1] == '"' {
						i++
						continue
					}
					inString = false
					verbatimString = false
				}
				continue
			}
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		if inChar {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '\'' {
				inChar = false
			}
			continue
		}
		if b == '/' && i+1 < end {
			switch source[i+1] {
			case '/':
				inLineComment = true
				i++
				continue
			case '*':
				inBlockComment = true
				i++
				continue
			}
		}
		switch b {
		case '"':
			inString = true
			verbatimString = (i > start && source[i-1] == '@') ||
				(i > start+1 && source[i-1] == '$' && source[i-2] == '@')
			escape = false
			continue
		case '\'':
			inChar = true
			escape = false
			continue
		case '#':
			for i < end && source[i] != '\n' {
				i++
			}
			continue
		}
		if b == 'n' && csharpHasKeywordAt(source, i, "namespace") {
			afterOK := true
			if after := i + uint32(len("namespace")); after < end && csharpIdentifierContinueByte(source[after]) {
				afterOK = false
			}
			beforeOK := i == start || !csharpIdentifierContinueByte(source[i-1])
			if afterOK && beforeOK {
				return i, true
			}
		}
	}
	return 0, false
}

// csharpReplaceNodeContents overwrites dst's node contents (symbol, span,
// children, ...) with src's while preserving dst's position in its parent.
// Shared by the invocation, statement, and string-interpolation recovery
// rewrites.
func csharpReplaceNodeContents(dst, src *Node) {
	if dst == nil || src == nil {
		return
	}
	parent := dst.parent
	childIndex := dst.childIndex
	*dst = *src
	dst.parent = parent
	dst.childIndex = childIndex
	populateParentNode(dst, dst.children)
}
