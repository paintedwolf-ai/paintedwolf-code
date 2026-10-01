package gotreesitter

import "testing"

func TestNormalizePerlPushExpressionListsRewritesRootListShape(t *testing.T) {
	lang := &Language{
		Name:        "perl",
		SymbolNames: []string{"EOF", "source_file", "expression_statement", "ambiguous_function_call_expression", "function", "list_expression", ",", "array", "scalar"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "expression_statement", Visible: true, Named: true},
			{Name: "ambiguous_function_call_expression", Visible: true, Named: true},
			{Name: "function", Visible: true, Named: true},
			{Name: "list_expression", Visible: true, Named: true},
			{Name: ",", Visible: true, Named: false},
			{Name: "array", Visible: true, Named: true},
			{Name: "scalar", Visible: true, Named: true},
		},
	}

	source := []byte("push @found, $_")
	arena := newNodeArena(arenaClassFull)

	fn := newLeafNodeInArena(arena, 4, true, 0, 4, Point{Row: 0, Column: 0}, Point{Row: 0, Column: 4})
	arg0 := newLeafNodeInArena(arena, 7, true, 5, 11, Point{Row: 0, Column: 5}, Point{Row: 0, Column: 11})
	comma := newLeafNodeInArena(arena, 6, false, 11, 12, Point{Row: 0, Column: 11}, Point{Row: 0, Column: 12})
	arg1 := newLeafNodeInArena(arena, 8, true, 13, 15, Point{Row: 0, Column: 13}, Point{Row: 0, Column: 15})

	call := newParentNodeInArena(arena, 3, true, []*Node{fn, arg0}, nil, 0)
	list := newParentNodeInArena(arena, 5, true, []*Node{call, comma, arg1}, nil, 0)
	stmt := newParentNodeInArena(arena, 2, true, []*Node{list}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{stmt}, nil, 0)

	normalizePerlPushExpressionLists(root, source, lang)

	rewritten := stmt.Child(0)
	if rewritten == nil {
		t.Fatal("expression_statement lost child after normalization")
	}
	if got := rewritten.Type(lang); got != "ambiguous_function_call_expression" {
		t.Fatalf("rewritten child type = %q, want ambiguous_function_call_expression", got)
	}
	if got, want := rewritten.ChildCount(), 2; got != want {
		t.Fatalf("rewritten child count = %d, want %d", got, want)
	}
	args := rewritten.Child(1)
	if args == nil || args.Type(lang) != "list_expression" {
		t.Fatalf("rewritten arguments = %v, want list_expression", args)
	}
	if got, want := args.ChildCount(), 3; got != want {
		t.Fatalf("rewritten args child count = %d, want %d", got, want)
	}
	if got := args.Child(0).Type(lang); got != "array" {
		t.Fatalf("rewritten first arg type = %q, want array", got)
	}
	if got := args.Child(2).Type(lang); got != "scalar" {
		t.Fatalf("rewritten third arg type = %q, want scalar", got)
	}
}
