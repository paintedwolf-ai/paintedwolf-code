package gotreesitter

func bytesAreTrivia(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return false
		}
	}
	return true
}

// firstNonTriviaByteStart returns the first non-whitespace byte. It skips an
// optional UTF-8 byte order mark. It returns zero for an all-whitespace source.
func firstNonTriviaByteStart(source []byte) uint32 {
	start := 0
	if len(source) >= len(utf8BOM) &&
		source[0] == utf8BOM[0] && source[1] == utf8BOM[1] && source[2] == utf8BOM[2] {
		start = len(utf8BOM)
	}
	for index := start; index < len(source); index++ {
		switch source[index] {
		case ' ', '\t', '\n', '\r', '\f':
			continue
		default:
			return uint32(index)
		}
	}
	return 0
}

// bytesAreInterTokenTrivia is bytesAreTrivia plus line-continuation backslashes
// (`\` immediately before a newline). The forest's reduce-coverage rejection
// uses it to decide whether a hole BETWEEN two reduced children is real
// (a dropped statement/token → invalid) or just inter-token whitespace the
// lexer skips. A bare `\<newline>` only appears between tokens where the
// language treats it as a continuation (bash/python/C/…), so accepting it here
// is safe; a real `\` token is never immediately followed by a newline.
func bytesAreInterTokenTrivia(b []byte) bool {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case ' ', '\t', '\n', '\r', '\f':
			continue
		case '\\':
			if i+1 < len(b) && (b[i+1] == '\n' || b[i+1] == '\r') {
				continue // line-continuation backslash; the newline is whitespace
			}
			return false
		default:
			return false
		}
	}
	return true
}

func lastNonTriviaByteEnd(source []byte) uint32 {
	for i := len(source); i > 0; i-- {
		switch source[i-1] {
		case ' ', '\t', '\n', '\r', '\f':
			continue
		default:
			return uint32(i)
		}
	}
	return 0
}

// filterHiddenExtraTriviaRoot removes hidden, childless whitespace extras from
// a clean root. The root keeps its produced span. Visible extras remain public.
// Error roots keep every extra to preserve recovery evidence.
func filterHiddenExtraTriviaRoot(root *Node, source []byte, lang *Language) {
	view := resultMutableChildrenForMutation(root)
	childCount := view.Len()
	if root == nil || childCount == 0 || len(source) == 0 {
		return
	}
	if view.hasFinalChildRefs() {
		found := false
		for i := 0; i < childCount; i++ {
			entry, ok := view.Entry(i)
			if ok && rootEntryIsHiddenExtraTrivia(root, entry, source, lang) {
				found = true
				break
			}
		}
		if !found {
			return
		}
		view.FilterFinalRefs(func(_ int, entry stackEntry) bool {
			return !rootEntryIsHiddenExtraTrivia(root, entry, source, lang)
		})
		return
	}
	fieldIDs := root.fieldIDs()
	fieldSources := root.fieldSources()
	if (len(fieldIDs) != 0 && len(fieldIDs) != childCount) ||
		(len(fieldSources) != 0 && len(fieldSources) != childCount) {
		return
	}
	children := resultChildSliceForMutation(root)
	found := false
	for _, child := range children {
		if rootNodeIsHiddenExtraTrivia(root, child, source, lang) {
			found = true
			break
		}
	}
	if !found {
		return
	}
	keptIndexes := make([]int, 0, len(children))
	for i, child := range children {
		if rootNodeIsHiddenExtraTrivia(root, child, source, lang) {
			continue
		}
		keptIndexes = append(keptIndexes, i)
	}
	filtered := make([]*Node, len(keptIndexes))
	for i, oldIndex := range keptIndexes {
		filtered[i] = children[oldIndex]
	}
	root.children = cloneNodeSliceInArena(root.ownerArena, filtered)
	var filteredFieldIDs []FieldID
	if len(fieldIDs) == childCount {
		filteredFieldIDs = cloneFieldIDSliceInArena(root.ownerArena, make([]FieldID, len(keptIndexes)))
		for i, oldIndex := range keptIndexes {
			filteredFieldIDs[i] = fieldIDs[oldIndex]
		}
	}
	var filteredFieldSources []uint8
	if len(fieldSources) == childCount {
		if root.ownerArena != nil {
			filteredFieldSources = root.ownerArena.allocFieldSourceSlice(len(keptIndexes))
		} else {
			filteredFieldSources = make([]uint8, len(keptIndexes))
		}
		for i, oldIndex := range keptIndexes {
			filteredFieldSources[i] = fieldSources[oldIndex]
		}
	}
	root.setFieldMetadata(filteredFieldIDs, filteredFieldSources)
	for i, child := range root.children {
		setNodeParentLink(child, root, i)
	}
}

func rootEntryIsHiddenExtraTrivia(root *Node, entry stackEntry, source []byte, lang *Language) bool {
	if !stackEntryNodeIsExtra(entry) || stackEntryNodeChildCount(entry) != 0 {
		return false
	}
	return rootRangeIsHiddenExtraTrivia(
		root,
		stackEntryNodeSymbol(entry),
		stackEntryNodeStartByte(entry),
		stackEntryNodeEndByte(entry),
		source,
		lang,
	)
}

func rootNodeIsHiddenExtraTrivia(root, child *Node, source []byte, lang *Language) bool {
	if child == nil || !child.IsExtra() || resultChildCount(child) != 0 {
		return false
	}
	return rootRangeIsHiddenExtraTrivia(
		root,
		child.symbol,
		child.startByte,
		child.endByte,
		source,
		lang,
	)
}

func rootRangeIsHiddenExtraTrivia(
	root *Node,
	symbol Symbol,
	start, end uint32,
	source []byte,
	lang *Language,
) bool {
	if root == nil || symbolIsVisible(lang, symbol) || start >= end {
		return false
	}
	if int(end) > len(source) {
		return false
	}
	return bytesAreTrivia(source[start:end])
}
