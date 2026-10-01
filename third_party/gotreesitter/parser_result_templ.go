package gotreesitter

import "bytes"

func normalizeTemplCompatibility(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "templ" || len(source) == 0 || root.HasError() {
		return
	}
	normalizeTemplComponentImportArguments(root, source, lang)
	normalizeTemplTagStartDanglingQuoteErrors(root, source, lang)
}

func normalizeTemplTagStartDanglingQuoteErrors(root *Node, source []byte, lang *Language) {
	walkResultTree(root, func(n *Node) {
		if n == nil || n.ownerArena == nil || n.Type(lang) != "tag_start" || n.HasError() {
			return
		}
		children := resultChildSliceForMutation(n)
		if len(children) < 3 {
			return
		}
		closeIdx := len(children) - 1
		close := children[closeIdx]
		prev := children[closeIdx-1]
		if close == nil || prev == nil || close.Type(lang) != ">" || prev.Type(lang) != "attribute" {
			return
		}
		if prev.endByte >= close.startByte || int(close.startByte) > len(source) {
			return
		}
		errStart := prev.endByte
		if errStart >= uint32(len(source)) || (source[errStart] != '"' && source[errStart] != '\'') {
			return
		}
		quoteSym, quoteNamed, ok := symbolMeta(lang, string(source[errStart]))
		if !ok {
			return
		}
		for i := errStart + 1; i < close.startByte; i++ {
			if source[i] != ' ' && source[i] != '\t' && source[i] != '\r' && source[i] != '\n' {
				return
			}
		}
		quote := templNewLeaf(n.ownerArena, source, quoteSym, quoteNamed, errStart, errStart+1)
		err := newParentNodeInArena(n.ownerArena, errorSymbol, true, cloneNodeSliceInArena(n.ownerArena, []*Node{quote}), nil, 0)
		err.setHasError(true)
		out := make([]*Node, 0, len(children)+1)
		out = append(out, children[:closeIdx]...)
		out = append(out, err)
		out = append(out, children[closeIdx:]...)
		replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(n.ownerArena, out))
		templMarkErrorAncestors(n)
	})
}

func normalizeTemplComponentImportArguments(root *Node, source []byte, lang *Language) {
	argListSym, argListNamed, hasArgList := symbolMeta(lang, "argument_list")
	walkResultTree(root, func(n *Node) {
		if n == nil || n.HasError() {
			return
		}
		children := resultChildSliceForMutation(n)
		if len(children) < 2 {
			return
		}
		changed := false
		out := make([]*Node, 0, len(children))
		for i := 0; i < len(children); i++ {
			child := children[i]
			if i+1 < len(children) && templCanMergeComponentImportArgs(child, children[i+1], source, lang) {
				args := children[i+1]
				if !hasArgList {
					out = append(out, child)
					continue
				}
				argList := templBuildSimpleArgumentList(args, source, lang, argListSym, argListNamed)
				if argList == nil {
					out = append(out, child)
					continue
				}
				replaceNodeChildrenUnfielded(child, cloneNodeSliceInArena(child.ownerArena, append(resultChildSliceForMutation(child), argList)))
				child.endByte = args.endByte
				child.endPoint = args.endPoint
				out = append(out, child)
				i++
				changed = true
				continue
			}
			if i+1 < len(children) && templCanMergeQualifiedComponentImport(child, children[i+1], source, lang) {
				args := children[i+1]
				rewritten := templBuildQualifiedComponentImport(child, args, source, lang, argListSym, argListNamed, hasArgList)
				if rewritten == nil {
					out = append(out, child)
					continue
				}
				out = append(out, rewritten)
				i++
				changed = true
				continue
			}
			out = append(out, child)
		}
		if changed {
			replaceNodeChildrenUnfielded(n, cloneNodeSliceInArena(n.ownerArena, out))
		}
	})
}

func templCanMergeComponentImportArgs(importNode, argsNode *Node, source []byte, lang *Language) bool {
	if importNode == nil || argsNode == nil {
		return false
	}
	if importNode.Type(lang) != "component_import" || argsNode.Type(lang) != "element_text" {
		return false
	}
	if importNode.endByte != argsNode.startByte || argsNode.startByte >= argsNode.endByte {
		return false
	}
	if int(argsNode.endByte) > len(source) {
		return false
	}
	args := source[argsNode.startByte:argsNode.endByte]
	return len(args) >= 2 && args[0] == '(' && args[len(args)-1] == ')'
}

func templCanMergeQualifiedComponentImport(importNode, tailNode *Node, source []byte, lang *Language) bool {
	if importNode == nil || tailNode == nil {
		return false
	}
	if importNode.Type(lang) != "component_import" || tailNode.Type(lang) != "element_text" {
		return false
	}
	if importNode.endByte != tailNode.startByte || tailNode.startByte >= tailNode.endByte {
		return false
	}
	if int(tailNode.endByte) > len(source) {
		return false
	}
	children := resultChildSliceForMutation(importNode)
	if len(children) != 2 || children[1] == nil || children[1].Type(lang) != "component_identifier" {
		return false
	}
	tail := source[tailNode.startByte:tailNode.endByte]
	return templSplitQualifiedImportTail(tail) != nil
}

type templQualifiedImportTail struct {
	nameStart int
	nameEnd   int
	argsStart int
}

func templSplitQualifiedImportTail(tail []byte) *templQualifiedImportTail {
	if len(tail) < 4 || tail[0] != '.' {
		return nil
	}
	if bytes.IndexByte(tail, '\n') >= 0 || bytes.IndexByte(tail, '\r') >= 0 {
		return nil
	}
	i := 1
	if !templIsIdentifierStart(tail[i]) {
		return nil
	}
	nameStart := i
	i++
	for i < len(tail) && templIsIdentifierContinue(tail[i]) {
		i++
	}
	if i >= len(tail) || tail[i] != '(' || tail[len(tail)-1] != ')' {
		return nil
	}
	return &templQualifiedImportTail{nameStart: nameStart, nameEnd: i, argsStart: i}
}

func templBuildQualifiedComponentImport(importNode, tailNode *Node, source []byte, lang *Language, argListSym Symbol, argListNamed bool, hasArgList bool) *Node {
	if importNode == nil || tailNode == nil || !hasArgList || int(tailNode.endByte) > len(source) {
		return nil
	}
	dotSym, dotNamed, ok := symbolMeta(lang, ".")
	if !ok {
		return nil
	}
	packageSym, packageNamed, ok := symbolMeta(lang, "package_identifier")
	if !ok {
		return nil
	}
	componentSym, componentNamed, ok := symbolMeta(lang, "component_identifier")
	if !ok {
		return nil
	}
	tail := source[tailNode.startByte:tailNode.endByte]
	parts := templSplitQualifiedImportTail(tail)
	if parts == nil {
		return nil
	}
	children := resultChildSliceForMutation(importNode)
	if len(children) != 2 || children[1] == nil {
		return nil
	}
	arena := importNode.ownerArena
	pkg := newLeafNodeInArena(arena, packageSym, packageNamed, children[1].startByte, children[1].endByte, children[1].startPoint, children[1].endPoint)
	dot := newLeafNodeInArena(arena, dotSym, dotNamed, tailNode.startByte, tailNode.startByte+1, tailNode.startPoint, Point{Row: tailNode.startPoint.Row, Column: tailNode.startPoint.Column + 1})
	nameStart := tailNode.startByte + uint32(parts.nameStart)
	nameEnd := tailNode.startByte + uint32(parts.nameEnd)
	name := newLeafNodeInArena(arena, componentSym, componentNamed, nameStart, nameEnd, Point{Row: tailNode.startPoint.Row, Column: tailNode.startPoint.Column + uint32(parts.nameStart)}, Point{Row: tailNode.startPoint.Row, Column: tailNode.startPoint.Column + uint32(parts.nameEnd)})

	argsView := *tailNode
	argsView.startByte = tailNode.startByte + uint32(parts.argsStart)
	argsView.startPoint = Point{Row: tailNode.startPoint.Row, Column: tailNode.startPoint.Column + uint32(parts.argsStart)}
	argsView.children = nil
	argsView.clearFieldMetadata()
	argList := templBuildSimpleArgumentList(&argsView, source, lang, argListSym, argListNamed)
	if argList == nil {
		return nil
	}
	fields := templComponentImportFieldIDs(arena, lang)
	return newParentNodeInArena(arena, importNode.symbol, importNode.isNamed(), cloneNodeSliceInArena(arena, []*Node{children[0], pkg, dot, name, argList}), fields, importNode.productionID)
}

func templComponentImportFieldIDs(arena *nodeArena, lang *Language) []FieldID {
	if lang == nil {
		return nil
	}
	fields := make([]FieldID, 5)
	if fid, ok := lang.FieldByName("package"); ok {
		fields[1] = fid
	}
	if fid, ok := lang.FieldByName("name"); ok {
		fields[3] = fid
	}
	if fid, ok := lang.FieldByName("arguments"); ok {
		fields[4] = fid
	}
	return cloneFieldIDSliceInArena(arena, fields)
}

func templBuildSimpleArgumentList(argsNode *Node, source []byte, lang *Language, argListSym Symbol, argListNamed bool) *Node {
	if argsNode == nil || int(argsNode.endByte) > len(source) {
		return nil
	}
	openSym, openNamed, ok := symbolMeta(lang, "(")
	if !ok {
		return nil
	}
	closeSym, closeNamed, ok := symbolMeta(lang, ")")
	if !ok {
		return nil
	}
	commaSym, commaNamed, ok := symbolMeta(lang, ",")
	if !ok {
		return nil
	}
	identSym, identNamed, ok := symbolMeta(lang, "identifier")
	if !ok {
		return nil
	}
	stringSym, stringNamed, hasString := symbolMeta(lang, "interpreted_string_literal")
	stringContentSym, stringContentNamed, hasStringContent := symbolMeta(lang, "interpreted_string_literal_content")
	quoteSym, quoteNamed, hasQuote := symbolMeta(lang, "\"")
	raw := source[argsNode.startByte:argsNode.endByte]
	if bytes.IndexByte(raw, '\n') >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
		return nil
	}
	if len(raw) < 2 || raw[0] != '(' || raw[len(raw)-1] != ')' {
		return nil
	}

	arena := argsNode.ownerArena
	children := make([]*Node, 0, 5)
	start := argsNode.startByte
	children = append(children, newLeafNodeInArena(arena, openSym, openNamed, start, start+1, argsNode.startPoint, Point{Row: argsNode.startPoint.Row, Column: argsNode.startPoint.Column + 1}))
	i := 1
	for i < len(raw)-1 {
		for i < len(raw)-1 && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw)-1 {
			break
		}
		if raw[i] == ',' {
			pos := start + uint32(i)
			children = append(children, newLeafNodeInArena(arena, commaSym, commaNamed, pos, pos+1, Point{Row: argsNode.startPoint.Row, Column: argsNode.startPoint.Column + uint32(i)}, Point{Row: argsNode.startPoint.Row, Column: argsNode.startPoint.Column + uint32(i) + 1}))
			i++
			continue
		}
		if raw[i] == '"' {
			if !hasString || !hasStringContent || !hasQuote {
				return nil
			}
			str, next, ok := templBuildSimpleStringLiteral(argsNode, source, lang, i, stringSym, stringNamed, stringContentSym, stringContentNamed, quoteSym, quoteNamed)
			if !ok {
				return nil
			}
			children = append(children, str)
			i = next
			continue
		}
		if !templIsIdentifierStart(raw[i]) {
			return nil
		}
		identStart := i
		i++
		for i < len(raw)-1 && templIsIdentifierContinue(raw[i]) {
			i++
		}
		posStart := start + uint32(identStart)
		posEnd := start + uint32(i)
		children = append(children, newLeafNodeInArena(arena, identSym, identNamed, posStart, posEnd, Point{Row: argsNode.startPoint.Row, Column: argsNode.startPoint.Column + uint32(identStart)}, Point{Row: argsNode.startPoint.Row, Column: argsNode.startPoint.Column + uint32(i)}))
	}
	closeStart := argsNode.endByte - 1
	children = append(children, newLeafNodeInArena(arena, closeSym, closeNamed, closeStart, argsNode.endByte, Point{Row: argsNode.endPoint.Row, Column: argsNode.endPoint.Column - 1}, argsNode.endPoint))
	return newParentNodeInArena(arena, argListSym, argListNamed, cloneNodeSliceInArena(arena, children), nil, 0)
}

func templNewLeaf(arena *nodeArena, source []byte, sym Symbol, named bool, start, end uint32) *Node {
	return newLeafNodeInArena(arena, sym, named, start, end,
		advancePointByBytes(Point{}, source[:start]),
		advancePointByBytes(Point{}, source[:end]))
}

func templMarkErrorAncestors(n *Node) {
	for cur := n; cur != nil; cur = cur.parent {
		cur.setHasError(true)
		nodeBumpEquivVersion(cur)
	}
}

func templBuildSimpleStringLiteral(argsNode *Node, source []byte, lang *Language, relStart int, stringSym Symbol, stringNamed bool, contentSym Symbol, contentNamed bool, quoteSym Symbol, quoteNamed bool) (*Node, int, bool) {
	raw := source[argsNode.startByte:argsNode.endByte]
	if relStart >= len(raw)-1 || raw[relStart] != '"' {
		return nil, relStart, false
	}
	relEnd := relStart + 1
	for relEnd < len(raw)-1 && raw[relEnd] != '"' {
		if raw[relEnd] == '\\' {
			return nil, relStart, false
		}
		relEnd++
	}
	if relEnd >= len(raw)-1 || raw[relEnd] != '"' {
		return nil, relStart, false
	}
	arena := argsNode.ownerArena
	row := argsNode.startPoint.Row
	baseCol := argsNode.startPoint.Column
	start := argsNode.startByte + uint32(relStart)
	end := argsNode.startByte + uint32(relEnd) + 1
	open := newLeafNodeInArena(arena, quoteSym, quoteNamed, start, start+1, Point{Row: row, Column: baseCol + uint32(relStart)}, Point{Row: row, Column: baseCol + uint32(relStart) + 1})
	content := newLeafNodeInArena(arena, contentSym, contentNamed, start+1, end-1, Point{Row: row, Column: baseCol + uint32(relStart) + 1}, Point{Row: row, Column: baseCol + uint32(relEnd)})
	close := newLeafNodeInArena(arena, quoteSym, quoteNamed, end-1, end, Point{Row: row, Column: baseCol + uint32(relEnd)}, Point{Row: row, Column: baseCol + uint32(relEnd) + 1})
	str := newParentNodeInArena(arena, stringSym, stringNamed, []*Node{open, content, close}, nil, 0)
	return str, relEnd + 1, true
}

func templIsIdentifierStart(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_'
}

func templIsIdentifierContinue(b byte) bool {
	return templIsIdentifierStart(b) || (b >= '0' && b <= '9')
}
