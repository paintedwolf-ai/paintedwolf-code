package gotreesitter

import "testing"

func wolframCompatLang() *Language {
	return &Language{
		Name:        "wolfram",
		SymbolNames: []string{"EOF", "source_file", "symbol", "prefix", "+", "-", "infix", "integer", "real", "string"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "symbol", Visible: true, Named: true},
			{Name: "prefix", Visible: true, Named: true},
			{Name: "+", Visible: true, Named: false},
			{Name: "-", Visible: true, Named: false},
			{Name: "infix", Visible: true, Named: true},
			{Name: "integer", Visible: true, Named: true},
			{Name: "real", Visible: true, Named: true},
			{Name: "string", Visible: true, Named: true},
		},
	}
}

func TestNormalizeWolframSplitInfixRoot(t *testing.T) {
	lang := wolframCompatLang()
	arena := newNodeArena(arenaClassFull)
	source := []byte("   a + b\n")

	left := newLeafNodeInArena(arena, 2, true, 3, 4, Point{Row: 0, Column: 3}, Point{Row: 0, Column: 4})
	op := newLeafNodeInArena(arena, 4, false, 5, 6, Point{Row: 0, Column: 5}, Point{Row: 0, Column: 6})
	right := newLeafNodeInArena(arena, 2, true, 7, 8, Point{Row: 0, Column: 7}, Point{Row: 0, Column: 8})
	prefix := newParentNodeInArena(arena, 3, true, []*Node{op, right}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{left, prefix}, nil, 0)

	normalizeWolframCompatibility(root, source, lang)

	if got, want := resultChildCount(root), 1; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	infix := root.Child(0)
	if got, want := infix.Type(lang), "infix"; got != want {
		t.Fatalf("merged node type = %q, want %q", got, want)
	}
	if got, want := resultChildCount(infix), 3; got != want {
		t.Fatalf("infix child count = %d, want %d", got, want)
	}
	if got, want := infix.Child(0).Type(lang), "symbol"; got != want {
		t.Fatalf("left operand type = %q, want %q", got, want)
	}
	if got, want := infix.Child(1).Type(lang), "+"; got != want {
		t.Fatalf("operator type = %q, want %q", got, want)
	}
	if got, want := infix.Child(2).Type(lang), "symbol"; got != want {
		t.Fatalf("right operand type = %q, want %q", got, want)
	}
}

func TestNormalizeWolframSplitInfixRootLeavesUnaryPrefix(t *testing.T) {
	lang := wolframCompatLang()
	arena := newNodeArena(arenaClassFull)
	source := []byte("+b")

	op := newLeafNodeInArena(arena, 4, false, 0, 1, Point{}, Point{Column: 1})
	right := newLeafNodeInArena(arena, 2, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	prefix := newParentNodeInArena(arena, 3, true, []*Node{op, right}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{prefix}, nil, 0)

	normalizeWolframCompatibility(root, source, lang)

	if got, want := resultChildCount(root), 1; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	if got, want := root.Child(0).Type(lang), "prefix"; got != want {
		t.Fatalf("unary node type = %q, want %q", got, want)
	}
}
