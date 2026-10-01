package gotreesitter

import "testing"

func TestNormalizeSQLRecoveredSelectRootWrapsFlatSelectClause(t *testing.T) {
	lang := &Language{
		Name: "sql",
		SymbolNames: []string{
			"EOF", "source_file", "SELECT", "_aliasable_expression", "_expression", "type_cast",
			"select_clause_body_repeat1", ",", "comment", "select_statement", "select_clause",
			"select_clause_body", "NULL", "NULL",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "SELECT", Visible: true, Named: false},
			{Name: "_aliasable_expression", Visible: true, Named: true},
			{Name: "_expression", Visible: true, Named: true},
			{Name: "type_cast", Visible: true, Named: true},
			{Name: "select_clause_body_repeat1", Visible: true, Named: true},
			{Name: ",", Visible: true, Named: false},
			{Name: "comment", Visible: true, Named: true},
			{Name: "select_statement", Visible: true, Named: true},
			{Name: "select_clause", Visible: true, Named: true},
			{Name: "select_clause_body", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: false},
		},
	}

	arena := newNodeArena(arenaClassFull)
	selectTok := newLeafNodeInArena(arena, 2, false, 0, 6, Point{}, Point{Column: 6})
	firstExprLeaf := newLeafNodeInArena(arena, 5, true, 7, 8, Point{Column: 7}, Point{Column: 8})
	firstExpr := newParentNodeInArena(arena, 4, true, []*Node{firstExprLeaf}, nil, 0)
	firstAlias := newParentNodeInArena(arena, 3, true, []*Node{firstExpr}, nil, 0)
	comma1 := newLeafNodeInArena(arena, 7, false, 8, 9, Point{Column: 8}, Point{Column: 9})
	comment1 := newLeafNodeInArena(arena, 8, true, 10, 20, Point{Column: 10}, Point{Row: 1})
	secondExprLeaf := newLeafNodeInArena(arena, 5, true, 21, 22, Point{Row: 1}, Point{Row: 1, Column: 1})
	secondExpr := newParentNodeInArena(arena, 4, true, []*Node{secondExprLeaf}, nil, 0)
	secondAlias := newParentNodeInArena(arena, 3, true, []*Node{secondExpr}, nil, 0)
	repeat := newParentNodeInArena(arena, 6, true, []*Node{comma1, comment1, secondAlias}, nil, 0)
	comma2 := newLeafNodeInArena(arena, 7, false, 22, 23, Point{Row: 1, Column: 1}, Point{Row: 1, Column: 2})
	comment2 := newLeafNodeInArena(arena, 8, true, 24, 30, Point{Row: 1, Column: 3}, Point{Row: 1, Column: 9})
	root := newParentNodeInArena(arena, 1, true, []*Node{selectTok, firstAlias, repeat, comma2, comment2}, nil, 0)

	normalizeSQLRecoveredSelectRoot(root, lang)

	if got, want := len(root.children), 1; got != want {
		t.Fatalf("len(root.children) = %d, want %d", got, want)
	}
	if got, want := root.children[0].Type(lang), "select_statement"; got != want {
		t.Fatalf("root.children[0].Type = %q, want %q", got, want)
	}
	body := root.children[0].children[0].children[1]
	if got, want := body.Type(lang), "select_clause_body"; got != want {
		t.Fatalf("body.Type = %q, want %q", got, want)
	}
	if got, want := body.children[0].Type(lang), "type_cast"; got != want {
		t.Fatalf("body.children[0].Type = %q, want %q", got, want)
	}
	if got, want := body.children[len(body.children)-1].Type(lang), "NULL"; got != want {
		t.Fatalf("body.children[last].Type = %q, want %q", got, want)
	}
	if !root.HasError() {
		t.Fatalf("root.HasError = false, want true")
	}
}

func TestNormalizeSQLRecoveredSelectRootWrapsDirectTypeCastFragments(t *testing.T) {
	lang := &Language{
		Name: "sql",
		SymbolNames: []string{
			"EOF", "source_file", "SELECT", "type_cast", "select_clause_body_repeat1",
			",", "comment", "select_statement", "select_clause", "select_clause_body", "NULL", "NULL",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "SELECT", Visible: true, Named: false},
			{Name: "type_cast", Visible: true, Named: true},
			{Name: "select_clause_body_repeat1", Visible: false, Named: false},
			{Name: ",", Visible: true, Named: false},
			{Name: "comment", Visible: true, Named: true},
			{Name: "select_statement", Visible: true, Named: true},
			{Name: "select_clause", Visible: true, Named: true},
			{Name: "select_clause_body", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: false},
		},
	}

	arena := newNodeArena(arenaClassFull)
	selectTok := newLeafNodeInArena(arena, 2, false, 0, 6, Point{}, Point{Column: 6})
	firstTypeCast := newLeafNodeInArena(arena, 3, true, 9, 18, Point{Row: 1, Column: 2}, Point{Row: 1, Column: 11})
	comma1 := newLeafNodeInArena(arena, 5, false, 18, 19, Point{Row: 1, Column: 11}, Point{Row: 1, Column: 12})
	comment1 := newLeafNodeInArena(arena, 6, true, 20, 45, Point{Row: 2}, Point{Row: 2, Column: 25})
	secondTypeCast := newLeafNodeInArena(arena, 3, true, 48, 57, Point{Row: 3, Column: 2}, Point{Row: 3, Column: 11})
	repeat := newParentNodeInArena(arena, 4, false, []*Node{comma1, comment1, secondTypeCast}, nil, 0)
	comma2 := newLeafNodeInArena(arena, 5, false, 57, 58, Point{Row: 3, Column: 11}, Point{Row: 3, Column: 12})
	comment2 := newLeafNodeInArena(arena, 6, true, 59, 84, Point{Row: 4}, Point{Row: 4, Column: 25})
	root := newParentNodeInArena(arena, 1, true, []*Node{selectTok, firstTypeCast, repeat, comma2, comment2}, nil, 0)

	normalizeSQLRecoveredSelectRoot(root, lang)

	if got, want := len(root.children), 1; got != want {
		t.Fatalf("len(root.children) = %d, want %d", got, want)
	}
	body := root.children[0].children[0].children[1]
	if got, want := body.Type(lang), "select_clause_body"; got != want {
		t.Fatalf("body.Type = %q, want %q", got, want)
	}
	if got, want := body.children[0].Type(lang), "type_cast"; got != want {
		t.Fatalf("body.children[0].Type = %q, want %q", got, want)
	}
	if got, want := body.children[3].Type(lang), "type_cast"; got != want {
		t.Fatalf("body.children[3].Type = %q, want %q", got, want)
	}
	if got, want := body.children[len(body.children)-1].Type(lang), "NULL"; got != want {
		t.Fatalf("body.children[last].Type = %q, want %q", got, want)
	}
	if !root.HasError() {
		t.Fatalf("root.HasError = false, want true")
	}
}

func TestNormalizeSQLTrailingSelectListErrorFoldsRootRecovery(t *testing.T) {
	lang := &Language{
		Name: "sql",
		SymbolNames: []string{
			"EOF", "source_file", "SELECT", "type_cast", ",", "comment",
			"select_statement", "select_clause", "select_clause_body", "NULL", "NULL",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "SELECT", Visible: true, Named: false},
			{Name: "type_cast", Visible: true, Named: true},
			{Name: ",", Visible: true, Named: false},
			{Name: "comment", Visible: true, Named: true},
			{Name: "select_statement", Visible: true, Named: true},
			{Name: "select_clause", Visible: true, Named: true},
			{Name: "select_clause_body", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: true},
			{Name: "NULL", Visible: true, Named: false},
		},
	}

	arena := newNodeArena(arenaClassFull)
	selectTok := newLeafNodeInArena(arena, 2, false, 0, 6, Point{}, Point{Column: 6})
	typeCast := newLeafNodeInArena(arena, 3, true, 9, 18, Point{Row: 1, Column: 2}, Point{Row: 1, Column: 11})
	body := newParentNodeInArena(arena, 8, true, []*Node{typeCast}, nil, 0)
	selectClause := newParentNodeInArena(arena, 7, true, []*Node{selectTok, body}, nil, 0)
	stmt := newParentNodeInArena(arena, 6, true, []*Node{selectClause}, nil, 0)
	comma := newLeafNodeInArena(arena, 4, false, 18, 19, Point{Row: 1, Column: 11}, Point{Row: 1, Column: 12})
	errComma := newParentNodeInArena(arena, errorSymbol, true, []*Node{comma}, nil, 0)
	errComma.setHasError(true)
	errComma.setExtra(true)
	comment := newLeafNodeInArena(arena, 5, true, 20, 45, Point{Row: 2}, Point{Row: 2, Column: 25})
	comment.setExtra(true)
	root := newParentNodeInArena(arena, 1, true, []*Node{stmt, errComma, comment}, nil, 0)

	normalizeSQLTrailingSelectListError(root, lang)

	if got, want := len(root.children), 1; got != want {
		t.Fatalf("len(root.children) = %d, want %d", got, want)
	}
	body = root.children[0].children[0].children[1]
	if got, want := len(body.children), 4; got != want {
		t.Fatalf("len(body.children) = %d, want %d", got, want)
	}
	if got, want := body.children[1].Type(lang), ","; got != want {
		t.Fatalf("body.children[1].Type = %q, want %q", got, want)
	}
	if got, want := body.children[2].Type(lang), "comment"; got != want {
		t.Fatalf("body.children[2].Type = %q, want %q", got, want)
	}
	if got, want := body.children[3].Type(lang), "NULL"; got != want {
		t.Fatalf("body.children[3].Type = %q, want %q", got, want)
	}
	if !root.HasError() || !root.children[0].HasError() || !body.HasError() {
		t.Fatalf("expected root, select_statement, and body to retain errors")
	}
}
