package gotreesitter

import "testing"

func TestApplyReduceActionCollapsesUnarySelfReductionOnPrimaryStack(t *testing.T) {
	lang := &Language{
		SymbolCount:    2,
		TokenCount:     1,
		StateCount:     8,
		SymbolNames:    []string{"token", "statement"},
		SymbolMetadata: []SymbolMetadata{{Visible: true, Named: true}, {Visible: true, Named: true}},
		ParseTable: [][]uint16{
			{0, 1},
		},
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 7}}},
		},
	}
	parser := NewParser(lang)
	arena := acquireNodeArena(arenaClassFull)

	child := newParentNodeInArena(arena, 1, true, []*Node{
		newLeafNodeInArena(arena, errorSymbol, true, 0, 2, Point{}, Point{Column: 2}),
	}, nil, 11)
	child.parseState = 5
	child.preGotoState = 3

	s := newGLRStack(0)
	s.entries = append(s.entries, newStackEntryNode(5, child))

	act := ParseAction{Type: ParseActionReduce, Symbol: 1, ChildCount: 1, ProductionID: 23}
	tok := Token{Symbol: 0, StartByte: 2, EndByte: 3, StartPoint: Point{Column: 2}, EndPoint: Point{Column: 3}}
	anyReduced := false
	nodeCount := 1
	var entryScratch glrEntryScratch
	var gssScratch gssScratch

	parser.applyReduceAction(nil, &s, act, tok, &anyReduced, &nodeCount, arena, &entryScratch, &gssScratch, s.entries, false, false)

	if !anyReduced {
		t.Fatal("expected reduce to succeed")
	}
	if got, want := nodeCount, 1; got != want {
		t.Fatalf("nodeCount = %d, want %d", got, want)
	}
	if got, want := len(s.entries), 2; got != want {
		t.Fatalf("stack len = %d, want %d", got, want)
	}
	top := stackEntryNode(s.top())
	if top != child {
		t.Fatal("expected reduced stack to reuse child node")
	}
	if got, want := top.PreGotoState(), StateID(0); got != want {
		t.Fatalf("PreGotoState = %d, want %d", got, want)
	}
	if got, want := top.ParseState(), StateID(7); got != want {
		t.Fatalf("ParseState = %d, want %d", got, want)
	}
	if got, want := top.productionID, uint16(23); got != want {
		t.Fatalf("productionID = %d, want %d", got, want)
	}
}

func TestApplyReduceActionCollapsesUnarySelfReductionOnGSSStack(t *testing.T) {
	lang := &Language{
		SymbolCount:    2,
		TokenCount:     1,
		StateCount:     8,
		SymbolNames:    []string{"token", "statement"},
		SymbolMetadata: []SymbolMetadata{{Visible: true, Named: true}, {Visible: true, Named: true}},
		ParseTable: [][]uint16{
			{0, 1},
		},
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 7}}},
		},
	}
	parser := NewParser(lang)
	arena := acquireNodeArena(arenaClassFull)

	child := newParentNodeInArena(arena, 1, true, []*Node{
		newLeafNodeInArena(arena, errorSymbol, true, 0, 2, Point{}, Point{Column: 2}),
	}, nil, 11)
	child.parseState = 5
	child.preGotoState = 3

	var gssScratch gssScratch
	gss := newGSSStack(0, &gssScratch)
	gss.push(5, child, &gssScratch)
	s := glrStack{gss: gss}

	act := ParseAction{Type: ParseActionReduce, Symbol: 1, ChildCount: 1, ProductionID: 23}
	tok := Token{Symbol: 0, StartByte: 2, EndByte: 3, StartPoint: Point{Column: 2}, EndPoint: Point{Column: 3}}
	anyReduced := false
	nodeCount := 1
	var entryScratch glrEntryScratch
	var tmpEntries []stackEntry

	parser.applyReduceActionFromGSS(nil, &s, act, tok, &anyReduced, &nodeCount, arena, &entryScratch, &gssScratch, &tmpEntries, nil, false, false)

	if !anyReduced {
		t.Fatal("expected reduce to succeed")
	}
	if got, want := nodeCount, 1; got != want {
		t.Fatalf("nodeCount = %d, want %d", got, want)
	}
	if got, want := s.depth(), 2; got != want {
		t.Fatalf("stack depth = %d, want %d", got, want)
	}
	top := stackEntryNode(s.top())
	if top != child {
		t.Fatal("expected reduced stack to reuse child node")
	}
	if got, want := top.PreGotoState(), StateID(0); got != want {
		t.Fatalf("PreGotoState = %d, want %d", got, want)
	}
	if got, want := top.ParseState(), StateID(7); got != want {
		t.Fatalf("ParseState = %d, want %d", got, want)
	}
	if got, want := top.productionID, uint16(23); got != want {
		t.Fatalf("productionID = %d, want %d", got, want)
	}
}

func TestApplyReduceActionCollapsesInvisibleUnaryWrapper(t *testing.T) {
	lang := &Language{
		SymbolCount: 3,
		TokenCount:  1,
		StateCount:  8,
		SymbolNames: []string{"token", "visible_child", "_hidden_wrapper"},
		SymbolMetadata: []SymbolMetadata{
			{Visible: true, Named: true},
			{Visible: true, Named: true},
			{Visible: false, Named: false},
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
	defer arena.Release()

	child := newLeafNodeInArena(arena, 1, true, 0, 2, Point{}, Point{Column: 2})
	child.parseState = 5
	child.preGotoState = 3

	s := newGLRStack(0)
	s.entries = append(s.entries, newStackEntryNode(5, child))

	act := ParseAction{Type: ParseActionReduce, Symbol: 2, ChildCount: 1, ProductionID: 23}
	tok := Token{Symbol: 0, StartByte: 2, EndByte: 3, StartPoint: Point{Column: 2}, EndPoint: Point{Column: 3}}
	anyReduced := false
	nodeCount := 1
	var entryScratch glrEntryScratch
	var gssScratch gssScratch

	parser.applyReduceAction(nil, &s, act, tok, &anyReduced, &nodeCount, arena, &entryScratch, &gssScratch, s.entries, false, false)

	if !anyReduced {
		t.Fatal("expected reduce to succeed")
	}
	if got, want := nodeCount, 1; got != want {
		t.Fatalf("nodeCount = %d, want %d", got, want)
	}
	top := stackEntryNode(s.top())
	if top != child {
		t.Fatal("expected hidden wrapper reduce to reuse child node")
	}
	if got, want := top.Symbol(), Symbol(1); got != want {
		t.Fatalf("Symbol = %d, want %d", got, want)
	}
	if got, want := arena.parentNodesConstructed, uint64(0); got != want {
		t.Fatalf("parentNodesConstructed = %d, want %d", got, want)
	}
	if got, want := top.PreGotoState(), StateID(0); got != want {
		t.Fatalf("PreGotoState = %d, want %d", got, want)
	}
	if got, want := top.ParseState(), StateID(7); got != want {
		t.Fatalf("ParseState = %d, want %d", got, want)
	}
	if got, want := top.productionID, uint16(23); got != want {
		t.Fatalf("productionID = %d, want %d", got, want)
	}
}
