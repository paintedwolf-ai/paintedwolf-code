package gotreesitter

import "testing"

func TestFinalizeResultRootNativelyFiltersHiddenInteriorExtraTrivia(t *testing.T) {
	lang := trailingExtraNativeTestLanguage()
	source := []byte("# \nbody")
	parser := NewParser(lang)
	parser.noResultCompatibilityBenchmarkOnly = true
	arena := newNodeArena(arenaClassFull)
	comment := newLeafNodeInArena(arena, 4, true, 0, 1, Point{}, Point{Column: 1})
	comment.setExtra(true)
	trivia := newLeafNodeInArena(arena, 3, false, 1, 3, Point{Column: 1}, Point{Row: 1})
	trivia.setExtra(true)
	body := newLeafNodeInArena(arena, 2, true, 3, 7, Point{Row: 1}, Point{Row: 1, Column: 4})
	root := newParentNodeInArena(arena, 1, true, []*Node{comment, trivia, body}, nil, 0)
	root.setFieldMetadata(
		[]FieldID{1, 0, 2},
		[]uint8{fieldSourceDirect, fieldSourceNone, fieldSourceInherited},
	)

	parser.finalizeResultRoot(root, source, nil, false, true, nil)

	if got, want := root.ChildCount(), 2; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	if got := root.Child(0); got != comment || !got.IsExtra() {
		t.Fatalf("root child 0 = %#v, want visible comment extra", got)
	}
	if got := root.Child(1); got != body {
		t.Fatalf("root child 1 = %p, want body %p", got, body)
	}
	if got, want := root.StartByte(), uint32(0); got != want {
		t.Fatalf("root start byte = %d, want %d", got, want)
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("root end byte = %d, want %d", got, want)
	}
	if got, want := root.fieldIDs(), []FieldID{1, 2}; !equalFieldIDSlices(got, want) {
		t.Fatalf("root field IDs = %v, want %v", got, want)
	}
	if got, want := root.fieldSources(), []uint8{fieldSourceDirect, fieldSourceInherited}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("root field sources = %v, want %v", got, want)
	}
}

func TestFinalizeResultRootNativelyTrimsCleanTrailingExtraTrivia(t *testing.T) {
	lang := trailingExtraNativeTestLanguage()
	source := []byte("body\n")
	parser := NewParser(lang)
	parser.noResultCompatibilityBenchmarkOnly = true
	arena := newNodeArena(arenaClassFull)
	body := newLeafNodeInArena(arena, 2, true, 0, 4, Point{}, Point{Column: 4})
	trivia := newLeafNodeInArena(arena, 3, false, 4, 5, Point{Column: 4}, Point{Row: 1})
	trivia.setExtra(true)
	root := newParentNodeInArena(arena, 1, true, []*Node{body, trivia}, nil, 0)

	parser.finalizeResultRoot(root, source, nil, false, true, nil)

	if got, want := root.ChildCount(), 1; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	if got, want := root.Child(0), body; got != want {
		t.Fatalf("root child = %p, want body %p", got, want)
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("root end byte = %d, want %d", got, want)
	}
	if got, want := root.EndPoint(), (Point{Row: 1}); got != want {
		t.Fatalf("root end point = %+v, want %+v", got, want)
	}
}

func TestFinalizeResultRootKeepsErrorTrailingExtraTrivia(t *testing.T) {
	lang := trailingExtraNativeTestLanguage()
	source := []byte("body\n")
	parser := NewParser(lang)
	parser.noResultCompatibilityBenchmarkOnly = true
	arena := newNodeArena(arenaClassFull)
	body := newLeafNodeInArena(arena, 2, true, 0, 4, Point{}, Point{Column: 4})
	trivia := newLeafNodeInArena(arena, 3, false, 4, 5, Point{Column: 4}, Point{Row: 1})
	trivia.setExtra(true)
	root := newParentNodeInArena(arena, 1, true, []*Node{body, trivia}, nil, 0)
	root.setHasError(true)

	parser.finalizeResultRoot(root, source, nil, false, true, nil)

	if got, want := root.ChildCount(), 2; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	if got := root.Child(1); got == nil || !got.IsExtra() {
		t.Fatalf("root child 1 = %#v, want trailing extra", got)
	}
}

func trailingExtraNativeTestLanguage() *Language {
	return &Language{
		Name:        "generic_trivia",
		SymbolNames: []string{"EOF", "root", "body", "_trailing_trivia", "comment"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "root", Visible: true, Named: true},
			{Name: "body", Visible: true, Named: true},
			{Name: "_trailing_trivia"},
			{Name: "comment", Visible: true, Named: true},
		},
	}
}
