package gotreesitter

import "testing"

func TestAliasedHiddenSingleChildUsesLeafShape(t *testing.T) {
	lang := &Language{
		SymbolCount: 3,
		SymbolMetadata: []SymbolMetadata{
			{Visible: false, Named: false},
			{Visible: true, Named: false},
			{Visible: true, Named: true},
		},
	}
	arena := acquireNodeArena(arenaClassFull)

	leaf := newLeafNodeInArena(arena, 1, false, 4, 9, Point{Column: 4}, Point{Column: 9})
	hidden := newParentNodeInArena(arena, 0, false, []*Node{leaf}, nil, 17)

	aliased := aliasedNodeInArena(arena, lang, hidden, 2)
	if aliased == nil {
		t.Fatal("expected aliased node")
	}
	if got, want := aliased.symbol, Symbol(2); got != want {
		t.Fatalf("symbol = %d, want %d", got, want)
	}
	if got, want := aliased.ChildCount(), 0; got != want {
		t.Fatalf("child count = %d, want %d", got, want)
	}
	if !aliased.IsNamed() {
		t.Fatal("expected aliased node to inherit named metadata")
	}
	if got, want := aliased.StartByte(), uint32(4); got != want {
		t.Fatalf("start byte = %d, want %d", got, want)
	}
	if got, want := aliased.EndByte(), uint32(9); got != want {
		t.Fatalf("end byte = %d, want %d", got, want)
	}
}

func TestApplyReduceActionCollapsesNamedLeafWrapper(t *testing.T) {
	lang := &Language{
		SymbolCount: 3,
		TokenCount:  2,
		StateCount:  8,
		SymbolNames: []string{"false", "false", "expr"},
		SymbolMetadata: []SymbolMetadata{
			{Visible: true, Named: false},
			{Visible: true, Named: true},
			{Visible: true, Named: true},
		},
		ParseTable: [][]uint16{
			{0, 0, 1},
		},
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 7}}},
		},
	}
	parser := NewParser(lang)
	arena := acquireNodeArena(arenaClassFull)

	child := newLeafNodeInArena(arena, 0, false, 3, 8, Point{Column: 3}, Point{Column: 8})
	child.parseState = 5
	child.preGotoState = 3

	s := newGLRStack(0)
	s.entries = append(s.entries, newStackEntryNode(5, child))

	act := ParseAction{Type: ParseActionReduce, Symbol: 1, ChildCount: 1, ProductionID: 23}
	tok := Token{Symbol: 0, StartByte: 8, EndByte: 9, StartPoint: Point{Column: 8}, EndPoint: Point{Column: 9}}
	anyReduced := false
	nodeCount := 1
	var entryScratch glrEntryScratch
	var gssScratch gssScratch

	parser.applyReduceAction(nil, &s, act, tok, &anyReduced, &nodeCount, arena, &entryScratch, &gssScratch, s.entries, false, false)

	if !anyReduced {
		t.Fatal("expected reduce to succeed")
	}
	top := stackEntryNode(s.top())
	if top == nil {
		t.Fatal("expected top node")
	}
	if got, want := top.symbol, Symbol(1); got != want {
		t.Fatalf("symbol = %d, want %d", got, want)
	}
	if got, want := top.ChildCount(), 0; got != want {
		t.Fatalf("child count = %d, want %d", got, want)
	}
	if !top.IsNamed() {
		t.Fatal("expected wrapped leaf to become named")
	}
}
