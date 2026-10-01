package gotreesitter

import "testing"

func TestCSharpFindQueryAssignmentSpecs(t *testing.T) {
	src := []byte("var x = from a in source\n  where a.B == \"A\"\n  select new { Name = a.B };\n")

	specs, ok := csharpFindQueryAssignmentSpecs(src)
	if !ok {
		t.Fatal("expected query assignment spec")
	}
	if got := len(specs); got != 1 {
		t.Fatalf("spec count = %d, want 1", got)
	}
	if got := len(specs[0].clauses); got != 3 {
		t.Fatalf("clause count = %d, want 3", got)
	}
	if got := specs[0].clauses[0].kind; got != csharpQueryFromClause {
		t.Fatalf("first clause kind = %v, want from", got)
	}
	if got := specs[0].clauses[1].kind; got != csharpQueryWhereClause {
		t.Fatalf("second clause kind = %v, want where", got)
	}
	if got := specs[0].clauses[2].kind; got != csharpQuerySelectClause {
		t.Fatalf("third clause kind = %v, want select", got)
	}
}

func TestCSharpLargeNamespaceAltStatsCanSkipPrimary(t *testing.T) {
	child := csharpRecoveredDeclarationStats{total: 103, methods: 93}
	alt := csharpRecoveredDeclarationStats{total: 126, methods: 116}
	if !csharpLargeNamespaceAltStatsCanSkipPrimary(csharpLargeNamespaceRecoveryBodyBytes, child, alt) {
		t.Fatal("large, clear alt win should skip primary source recovery")
	}
	if csharpLargeNamespaceAltStatsCanSkipPrimary(csharpLargeNamespaceRecoveryBodyBytes-1, child, alt) {
		t.Fatal("small namespace must keep primary source recovery")
	}
	if csharpLargeNamespaceAltStatsCanSkipPrimary(csharpLargeNamespaceRecoveryBodyBytes, child, csharpRecoveredDeclarationStats{total: 110, methods: 96}) {
		t.Fatal("marginal alt win must keep primary source recovery")
	}
	if csharpLargeNamespaceAltStatsCanSkipPrimary(csharpLargeNamespaceRecoveryBodyBytes, child, csharpRecoveredDeclarationStats{total: 140, methods: 90}) {
		t.Fatal("alt with fewer methods must keep primary source recovery")
	}
}

func TestCSharpParseQueryExpressionSpecWithGroupIntoOrder(t *testing.T) {
	src := []byte("from a in sourceA\n" +
		"        join b in sourceB on a.FK equals b.PK\n" +
		"        group a by a.X into g\n" +
		"        orderby g ascending\n" +
		"        select new { A.A, B.B }")
	spec, ok := csharpParseQueryExpressionSpec(src, csharpQueryAssignmentSpec{
		queryStart: 0,
		queryEnd:   uint32(len(src)),
	})
	if !ok {
		t.Fatal("expected query expression spec")
	}
	if got, want := len(spec.clauses), 5; got != want {
		t.Fatalf("clause count = %d, want %d", got, want)
	}
}

func TestCSharpFirstStatementEndHandlesScopedLambda(t *testing.T) {
	src := []byte("    var l = scoped => null;\n    var l = (scoped i) => null;\n")
	got, ok := csharpFirstStatementEndInRange(src, 4, uint32(len(src)))
	if !ok {
		t.Fatal("expected statement span")
	}
	if want := uint32(len("    var l = scoped => null;")); got != want {
		t.Fatalf("statement end = %d, want %d", got, want)
	}
}

func TestCSharpFindTopLevelOperatorHandlesLambdaArrow(t *testing.T) {
	src := []byte("scoped => null")
	pos, ok := csharpFindTopLevelOperator(src, 0, uint32(len(src)), "=>")
	if !ok {
		t.Fatal("expected lambda arrow")
	}
	if want := uint32(len("scoped ")); pos != want {
		t.Fatalf("arrow pos = %d, want %d", pos, want)
	}
}

func TestCSharpTopLevelChunkSpansHandleAttributeCorpus(t *testing.T) {
	src := []byte("[A(B.C)]\n" +
		"class D {}\n\n" +
		"[NS.A(B.C)]\n" +
		"class D {}\n\n" +
		"[One][Two]\n" +
		"[Three]\n" +
		"class A { }\n\n" +
		"[A,B()][C]\n" +
		"struct A { }\n\n" +
		"class Zzz {\n" +
		"  [A,B()][C]\n" +
		"  public int Z;\n" +
		"}\n\n" +
		"class Methods {\n" +
		"  [ValidatedContract]\n" +
		"  int Method1() { return 0; }\n\n" +
		"  [method: ValidatedContract]\n" +
		"  int Method2() { return 0; }\n\n" +
		"  [return: ValidatedContract]\n" +
		"  int Method3() { return 0; }\n" +
		"}\n\n" +
		"[Single]\n" +
		"enum A { B, C }\n\n" +
		"class Zzz {\n" +
		"  [A,B()][C]\n" +
		"  public event EventHandler SomeEvent { add { } remove { } }\n" +
		"}\n\n" +
		"class Class<[A, B][C()]T1> {\n" +
		"  void Method<[E] [F, G(1)] T2>() {\n" +
		"  }\n" +
		"}\n\n" +
		"class Zzz {\n" +
		"  public event EventHandler SomeEvent {\n" +
		"    [A,B()][C] add { }\n" +
		"    [A,B()][C] remove { }\n" +
		"  }\n" +
		"}\n")
	spans := csharpTopLevelChunkSpans(src)
	if got, want := len(spans), 10; got != want {
		t.Fatalf("chunk span count = %d, want %d: %#v", got, want, spans)
	}
}

func TestNormalizeCSharpSurfaceCompatibilityKeepsNonTokenRepairs(t *testing.T) {
	lang := &Language{
		Name: "c_sharp",
		SymbolNames: []string{
			"EOF",
			"root",
			"modifier",
			"identifier",
			"lambda_expression",
			"argument",
			"string_literal",
			"interpolated_string_expression",
			"interpolation_start",
			"interpolation_end",
			"string_content",
			"\"",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "root", Visible: true, Named: true},
			{Name: "modifier", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "lambda_expression", Visible: true, Named: true},
			{Name: "argument", Visible: true, Named: true},
			{Name: "string_literal", Visible: true, Named: true},
			{Name: "interpolated_string_expression", Visible: true, Named: true},
			{Name: "interpolation_start", Visible: true, Named: true},
			{Name: "interpolation_end", Visible: true, Named: true},
			{Name: "string_content", Visible: true, Named: true},
			{Name: "\"", Visible: true, Named: false},
		},
		FieldNames: []string{"", "type"},
	}
	source := []byte("async $\"x\"")
	arena := newNodeArena(arenaClassFull)
	asyncIdent := newLeafNodeInArena(arena, 3, true, 0, 5, Point{}, Point{Column: 5})
	lambdaFields := cloneFieldIDSliceInArena(arena, []FieldID{1})
	lambda := newParentNodeInArena(arena, 4, true, []*Node{asyncIdent}, lambdaFields, 0)
	stringLiteral := newLeafNodeInArena(arena, 6, true, 7, 10, Point{Column: 7}, Point{Column: 10})
	argument := newParentNodeInArena(arena, 5, true, []*Node{stringLiteral}, nil, 0)
	argument.startByte = 7
	argument.startPoint = Point{Column: 7}
	root := newParentNodeInArena(arena, 1, true, []*Node{lambda, argument}, nil, 0)

	normalizeCSharpSurfaceCompatibility(root, source, lang)

	if got := lambda.FieldNameForChild(0, lang); got != "" {
		t.Fatalf("async lambda marker field = %q, want empty", got)
	}
	if got := asyncIdent.Type(lang); got != "modifier" {
		t.Fatalf("async lambda marker type = %q, want modifier", got)
	}
	if got := asyncIdent.ChildCount(); got != 0 {
		t.Fatalf("async modifier child count = %d, want 0", got)
	}
	if got, want := argument.StartByte(), uint32(6); got != want {
		t.Fatalf("interpolated argument start = %d, want %d", got, want)
	}
}

func TestNormalizeCSharpImplicitVarTypes(t *testing.T) {
	lang := &Language{
		Name: "c_sharp",
		SymbolNames: []string{
			"EOF",
			"root",
			"variable_declaration",
			"identifier",
			"implicit_type",
			"var",
			"declaration_pattern",
			"recursive_pattern",
			"positional_pattern_clause",
			"subpattern",
			"parenthesized_pattern",
			"(",
			")",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "root", Visible: true, Named: true},
			{Name: "variable_declaration", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "implicit_type", Visible: true, Named: true},
			{Name: "var", Visible: true, Named: false},
			{Name: "declaration_pattern", Visible: true, Named: true},
			{Name: "recursive_pattern", Visible: true, Named: true},
			{Name: "positional_pattern_clause", Visible: true, Named: true},
			{Name: "subpattern", Visible: true, Named: true},
			{Name: "parenthesized_pattern", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
		},
	}
	source := []byte("var x = y;")
	arena := newNodeArena(arenaClassFull)
	varIdent := newLeafNodeInArena(arena, 3, true, 0, 3, Point{}, Point{Column: 3})
	nameIdent := newLeafNodeInArena(arena, 3, true, 4, 5, Point{Column: 4}, Point{Column: 5})
	decl := newParentNodeInArena(arena, 2, true, []*Node{varIdent, nameIdent}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{decl}, nil, 0)

	normalizeCSharpImplicitVarTypes(root, source, lang)

	typeNode := decl.Child(0)
	if typeNode == nil || typeNode.Type(lang) != "implicit_type" {
		t.Fatalf("type child = %#v, want implicit_type", typeNode)
	}
	varTok := typeNode.Child(0)
	if varTok == nil || varTok.Type(lang) != "var" || varTok.IsNamed() {
		t.Fatalf("implicit type token = %#v, want anonymous var", varTok)
	}
	if got := decl.Child(1).Type(lang); got != "identifier" {
		t.Fatalf("name child type = %q, want identifier", got)
	}
}

func TestNormalizeCSharpParenthesizedVarPatterns(t *testing.T) {
	lang := &Language{
		Name: "c_sharp",
		SymbolNames: []string{
			"EOF",
			"root",
			"identifier",
			"implicit_type",
			"var",
			"declaration_pattern",
			"recursive_pattern",
			"positional_pattern_clause",
			"subpattern",
			"parenthesized_pattern",
			"(",
			")",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "root", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "implicit_type", Visible: true, Named: true},
			{Name: "var", Visible: true, Named: false},
			{Name: "declaration_pattern", Visible: true, Named: true},
			{Name: "recursive_pattern", Visible: true, Named: true},
			{Name: "positional_pattern_clause", Visible: true, Named: true},
			{Name: "subpattern", Visible: true, Named: true},
			{Name: "parenthesized_pattern", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
		},
	}
	source := []byte("(var a)")
	arena := newNodeArena(arenaClassFull)
	varIdent := newLeafNodeInArena(arena, 2, true, 1, 4, Point{Column: 1}, Point{Column: 4})
	nameIdent := newLeafNodeInArena(arena, 2, true, 5, 6, Point{Column: 5}, Point{Column: 6})
	decl := newParentNodeInArena(arena, 5, true, []*Node{varIdent, nameIdent}, nil, 0)
	subpattern := newParentNodeInArena(arena, 8, true, []*Node{decl}, nil, 0)
	clause := newParentNodeInArena(arena, 7, true, []*Node{subpattern}, nil, 0)
	recursive := newParentNodeInArena(arena, 6, true, []*Node{clause}, nil, 0)
	recursive.startByte = 0
	recursive.endByte = 7
	recursive.startPoint = Point{}
	recursive.endPoint = Point{Column: 7}
	root := newParentNodeInArena(arena, 1, true, []*Node{recursive}, nil, 0)

	normalizeCSharpImplicitVarTypes(root, source, lang)
	normalizeCSharpParenthesizedVarPatterns(root, source, lang)

	if got := recursive.Type(lang); got != "parenthesized_pattern" {
		t.Fatalf("pattern type = %q, want parenthesized_pattern", got)
	}
	if got := recursive.ChildCount(); got != 3 {
		t.Fatalf("pattern child count = %d, want 3", got)
	}
	if got := recursive.Child(1).Type(lang); got != "declaration_pattern" {
		t.Fatalf("pattern payload type = %q, want declaration_pattern", got)
	}
	if got := recursive.Child(1).Child(0).Type(lang); got != "implicit_type" {
		t.Fatalf("declaration type = %q, want implicit_type", got)
	}
}
