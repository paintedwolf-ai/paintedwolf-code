package gotreesitter

import "testing"

func TestAwkRecoveredShellQuoteRedirectMatchesCShape(t *testing.T) {
	lang := testAwkCompatLanguage()
	source := []byte("./echo 'Here===Is=Some=====Data' >foo")
	arena := newNodeArena(arenaClassFull)

	prefix := testAwkParent(arena, lang, "binary_exp", source, 0, 6, []*Node{
		testAwkLeaf(arena, lang, "number", source, 0, 1),
		testAwkLeaf(arena, lang, "/", source, 1, 2),
		testAwkLeaf(arena, lang, "identifier", source, 2, 6),
	})
	space := testAwkLeaf(arena, lang, "concatenating_space", source, 6, 7)
	openQuoteErr := testAwkError(arena, source, 7, 8, []*Node{
		testAwkLeaf(arena, lang, "ERROR", source, 7, 8),
	})
	hereErr := testAwkError(arena, source, 14, 15, []*Node{
		testAwkLeaf(arena, lang, "=", source, 14, 15),
	})
	trailingErr := testAwkError(arena, source, 24, 34, []*Node{
		testAwkLeaf(arena, lang, "==", source, 24, 26),
		testAwkLeaf(arena, lang, "=", source, 26, 27),
		testAwkLeaf(arena, lang, "identifier", source, 27, 31),
		testAwkLeaf(arena, lang, "ERROR", source, 31, 32),
		testAwkLeaf(arena, lang, ">", source, 33, 34),
	})
	someBinary := testAwkParent(arena, lang, "binary_exp", source, 18, 37, []*Node{
		testAwkLeaf(arena, lang, "identifier", source, 18, 22),
		testAwkLeaf(arena, lang, "==", source, 22, 24),
		trailingErr,
		testAwkLeaf(arena, lang, "identifier", source, 34, 37),
	})
	assignment := testAwkParent(arena, lang, "assignment_exp", source, 15, 37, []*Node{
		testAwkLeaf(arena, lang, "identifier", source, 15, 17),
		testAwkLeaf(arena, lang, "=", source, 17, 18),
		someBinary,
	})
	rightBinary := testAwkParent(arena, lang, "binary_exp", source, 8, 37, []*Node{
		testAwkLeaf(arena, lang, "identifier", source, 8, 12),
		testAwkLeaf(arena, lang, "==", source, 12, 14),
		hereErr,
		assignment,
	})
	concat := testAwkParent(arena, lang, "string_concat", source, 0, uint32(len(source)), []*Node{
		prefix,
		space,
		openQuoteErr,
		rightBinary,
	})

	got, ok := awkRecoveredShellQuoteRedirect(concat, source, lang, arena)
	if !ok {
		t.Fatal("awkRecoveredShellQuoteRedirect did not rewrite")
	}
	if got.Type(lang) != "binary_exp" {
		t.Fatalf("root type = %q, want binary_exp", got.Type(lang))
	}
	if got.Child(0).Type(lang) != "string_concat" || got.Child(1).Type(lang) != "ERROR" || got.Child(2).Type(lang) != ">" || got.Child(3).Type(lang) != "identifier" {
		t.Fatalf("outer children = %q %q %q %q", got.Child(0).Type(lang), got.Child(1).Type(lang), got.Child(2).Type(lang), got.Child(3).Type(lang))
	}
	if got.Child(0).EndByte() != 31 {
		t.Fatalf("left concat end = %d, want 31", got.Child(0).EndByte())
	}
	closeErr := got.Child(1)
	if !closeErr.IsExtra() || closeErr.StartByte() != 31 || closeErr.EndByte() != 32 || closeErr.ChildCount() != 2 {
		t.Fatalf("close ERROR = extra:%v span:%d..%d children:%d", closeErr.IsExtra(), closeErr.StartByte(), closeErr.EndByte(), closeErr.ChildCount())
	}
	innerAssignment := got.Child(0).Child(3).Child(3)
	if innerAssignment.Type(lang) != "assignment_exp" || innerAssignment.ChildCount() != 4 {
		t.Fatalf("inner assignment = %q/%d", innerAssignment.Type(lang), innerAssignment.ChildCount())
	}
	if innerAssignment.Child(1).Type(lang) != "ERROR" || !innerAssignment.Child(1).IsExtra() {
		t.Fatalf("assignment child[1] = %q extra:%v, want extra ERROR", innerAssignment.Child(1).Type(lang), innerAssignment.Child(1).IsExtra())
	}
	if got.fieldIDs()[0] != FieldID(1) || got.fieldIDs()[2] != FieldID(2) || got.fieldIDs()[3] != FieldID(3) {
		t.Fatalf("outer fields = %#v, want left/operator/right at 0/2/3", got.fieldIDs())
	}
	if innerAssignment.fieldIDs()[2] != 0 {
		t.Fatalf("assignment '=' field = %d, want 0", innerAssignment.fieldIDs()[2])
	}
}

func TestAwkRecoveredShellInRedirectMovesOperatorOutOfError(t *testing.T) {
	lang := testAwkCompatLanguage()
	source := []byte(".in >foo")
	arena := newNodeArena(arenaClassFull)
	inErr := testAwkError(arena, source, 1, 3, []*Node{
		testAwkLeaf(arena, lang, "in", source, 1, 3),
	})
	binary := testAwkParent(arena, lang, "binary_exp", source, 0, uint32(len(source)), []*Node{
		testAwkLeaf(arena, lang, "number", source, 0, 1),
		inErr,
		testAwkLeaf(arena, lang, ">", source, 4, 5),
		testAwkLeaf(arena, lang, "identifier", source, 5, 8),
	})

	got, ok := awkRecoveredShellInRedirect(binary, source, lang, arena)
	if !ok {
		t.Fatal("awkRecoveredShellInRedirect did not rewrite")
	}
	if got.Child(1).Type(lang) != "in" {
		t.Fatalf("child[1] = %q, want in", got.Child(1).Type(lang))
	}
	redirectErr := got.Child(2)
	if redirectErr.Type(lang) != "ERROR" || !redirectErr.IsExtra() || redirectErr.Child(0).Type(lang) != ">" {
		t.Fatalf("child[2] = %q extra:%v inner:%q, want extra ERROR wrapping >", redirectErr.Type(lang), redirectErr.IsExtra(), redirectErr.Child(0).Type(lang))
	}
	if got.fieldIDs()[1] != FieldID(2) {
		t.Fatalf("operator field = %d, want 2", got.fieldIDs()[1])
	}
	if got.fieldIDs()[2] != 0 {
		t.Fatalf("redirect ERROR field = %d, want 0", got.fieldIDs()[2])
	}
}

func testAwkCompatLanguage() *Language {
	names := []string{
		"EOF",
		"program",
		"rule",
		"pattern",
		"string_concat",
		"binary_exp",
		"assignment_exp",
		"number",
		"identifier",
		"concatenating_space",
		"/",
		"==",
		"=",
		">",
		"in",
	}
	meta := make([]SymbolMetadata, len(names))
	for i, name := range names {
		meta[i] = SymbolMetadata{Name: name, Visible: true, Named: true}
	}
	for _, name := range []string{"/", "==", "=", ">", "in"} {
		meta[testAwkSymbolByName(names, name)].Named = false
	}
	return &Language{
		Name:           "awk",
		SymbolNames:    names,
		SymbolMetadata: meta,
		FieldNames:     []string{"", "left", "operator", "right"},
	}
}

func testAwkLeaf(arena *nodeArena, lang *Language, name string, source []byte, start, end uint32) *Node {
	sym := errorSymbol
	named := true
	if name != "ERROR" {
		sym = Symbol(testAwkSymbolByName(lang.SymbolNames, name))
		named = symbolIsNamed(lang, sym)
	}
	return newLeafNodeInArena(arena, sym, named, start, end, advancePointByBytes(Point{}, source[:start]), advancePointByBytes(Point{}, source[:end]))
}

func testAwkParent(arena *nodeArena, lang *Language, name string, source []byte, start, end uint32, children []*Node) *Node {
	sym := Symbol(testAwkSymbolByName(lang.SymbolNames, name))
	node := newParentNodeInArena(arena, sym, symbolIsNamed(lang, sym), cloneNodeSliceInArena(arena, children), nil, 0)
	node.startByte = start
	node.startPoint = advancePointByBytes(Point{}, source[:start])
	node.endByte = end
	node.endPoint = advancePointByBytes(Point{}, source[:end])
	for _, child := range children {
		if child != nil && child.HasError() {
			node.setHasError(true)
		}
	}
	return node
}

func testAwkError(arena *nodeArena, source []byte, start, end uint32, children []*Node) *Node {
	node := newParentNodeInArena(arena, errorSymbol, true, cloneNodeSliceInArena(arena, children), nil, 0)
	node.startByte = start
	node.startPoint = advancePointByBytes(Point{}, source[:start])
	node.endByte = end
	node.endPoint = advancePointByBytes(Point{}, source[:end])
	node.setExtra(true)
	node.setHasError(true)
	return node
}

func testAwkSymbolByName(names []string, name string) int {
	for i, candidate := range names {
		if candidate == name {
			return i
		}
	}
	panic("missing test AWK symbol: " + name)
}
