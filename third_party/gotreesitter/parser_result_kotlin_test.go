package gotreesitter

import "testing"

func kotlinGenericCallTestLanguage() *Language {
	return &Language{
		Name: "kotlin",
		SymbolNames: []string{
			"EOF", "source_file", "comparison_expression", "call_expression",
			"call_suffix", "navigation_expression", "navigation_suffix",
			"simple_identifier", "type_identifier", "type_arguments",
			"type_projection", "user_type", "value_arguments", "value_argument",
			"parenthesized_expression", "annotated_lambda", "<", ">", "(", ")",
			".",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "comparison_expression", Visible: true, Named: true},
			{Name: "call_expression", Visible: true, Named: true},
			{Name: "call_suffix", Visible: true, Named: true},
			{Name: "navigation_expression", Visible: true, Named: true},
			{Name: "navigation_suffix", Visible: true, Named: true},
			{Name: "simple_identifier", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
			{Name: "type_arguments", Visible: true, Named: true},
			{Name: "type_projection", Visible: true, Named: true},
			{Name: "user_type", Visible: true, Named: true},
			{Name: "value_arguments", Visible: true, Named: true},
			{Name: "value_argument", Visible: true, Named: true},
			{Name: "parenthesized_expression", Visible: true, Named: true},
			{Name: "annotated_lambda", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: ">", Visible: true, Named: false},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: ".", Visible: true, Named: false},
		},
	}
}

func TestNormalizeKotlinGenericCallTypeArguments(t *testing.T) {
	lang := kotlinGenericCallTestLanguage()
	// Mirrors `tasks.named<KotlinCompile>("compile") {}` when GLR selects
	// comparisons for the angle brackets and a separate call for the suffix.
	source := []byte(`tasks.named<KotlinCompile>("compile") {}`)
	arena := newNodeArena(arenaClassFull)
	tasks := newLeafNodeInArena(arena, 7, true, 0, 5, Point{}, Point{Column: 5})
	dot := newLeafNodeInArena(arena, 20, false, 5, 6, Point{Column: 5}, Point{Column: 6})
	named := newLeafNodeInArena(arena, 7, true, 6, 11, Point{Column: 6}, Point{Column: 11})
	suffix := newParentNodeInArena(arena, 6, true, []*Node{dot, named}, nil, 0)
	nav := newParentNodeInArena(arena, 5, true, []*Node{tasks, suffix}, nil, 0)
	lt := newLeafNodeInArena(arena, 16, false, 11, 12, Point{Column: 11}, Point{Column: 12})
	typ := newLeafNodeInArena(arena, 7, true, 12, 25, Point{Column: 12}, Point{Column: 25})
	leftCmp := newParentNodeInArena(arena, 2, true, []*Node{nav, lt, typ}, nil, 0)
	gt := newLeafNodeInArena(arena, 17, false, 25, 26, Point{Column: 25}, Point{Column: 26})
	open := newLeafNodeInArena(arena, 18, false, 26, 27, Point{Column: 26}, Point{Column: 27})
	arg := newLeafNodeInArena(arena, 7, true, 27, 36, Point{Column: 27}, Point{Column: 36})
	close := newLeafNodeInArena(arena, 19, false, 36, 37, Point{Column: 36}, Point{Column: 37})
	paren := newParentNodeInArena(arena, 14, true, []*Node{open, arg, close}, nil, 0)
	lambda := newLeafNodeInArena(arena, 15, true, 38, 40, Point{Column: 38}, Point{Column: 40})
	trailingSuffix := newParentNodeInArena(arena, 4, true, []*Node{lambda}, nil, 0)
	tailCall := newParentNodeInArena(arena, 3, true, []*Node{paren, trailingSuffix}, nil, 0)
	call := newParentNodeInArena(arena, 2, true, []*Node{leftCmp, gt, tailCall}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{call}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := call.Type(lang), "call_expression"; got != want {
		t.Fatalf("node type = %q, want %q", got, want)
	}
	if got, want := call.ChildCount(), 2; got != want {
		t.Fatalf("call child count = %d, want %d", got, want)
	}
	gotSuffix := call.Child(1)
	if got, want := gotSuffix.Type(lang), "call_suffix"; got != want {
		t.Fatalf("suffix type = %q, want %q", got, want)
	}
	wantTypes := []string{"type_arguments", "value_arguments", "annotated_lambda"}
	for i, want := range wantTypes {
		if got := gotSuffix.Child(i).Type(lang); got != want {
			t.Fatalf("suffix child[%d] type = %q, want %q", i, got, want)
		}
	}
	if got := gotSuffix.Child(0).Child(1).Child(0).Child(0).Type(lang); got != "type_identifier" {
		t.Fatalf("type argument leaf = %q, want type_identifier", got)
	}
	if got := gotSuffix.Child(1).Child(1).Type(lang); got != "value_argument" {
		t.Fatalf("value argument wrapper = %q, want value_argument", got)
	}
	if p := gotSuffix.Parent(); p != call {
		t.Fatal("rewritten suffix parent not updated")
	}
}

func TestNormalizeKotlinRecoveredGenericCallTypeArguments(t *testing.T) {
	lang := kotlinGenericCallTestLanguage()
	source := []byte(`b<Map.Entry<Int, LongWrapper>>(Iterable { map.entries.iterator() })`)
	arena := newNodeArena(arenaClassFull)
	base := newLeafNodeInArena(arena, 7, true, 0, 1, Point{}, Point{Column: 1})
	lt := newLeafNodeInArena(arena, 16, false, 1, 2, Point{Column: 1}, Point{Column: 2})
	typ := newLeafNodeInArena(arena, 8, true, 2, 28, Point{Column: 2}, Point{Column: 28})
	gt := newLeafNodeInArena(arena, 17, false, 28, 29, Point{Column: 28}, Point{Column: 29})
	typeArgs := newParentNodeInArena(arena, 9, true, []*Node{lt, typ, gt}, nil, 0)
	errNode := newParentNodeInArena(arena, errorSymbol, true, []*Node{typeArgs}, nil, 0)
	errNode.setExtra(true)
	errNode.setHasError(true)
	valueArgs := newLeafNodeInArena(arena, 12, true, 29, 65, Point{Column: 29}, Point{Column: 65})
	suffix := newParentNodeInArena(arena, 4, true, []*Node{valueArgs}, nil, 0)
	call := newParentNodeInArena(arena, 3, true, []*Node{base, errNode, suffix}, nil, 0)
	call.setHasError(true)
	root := newParentNodeInArena(arena, 1, true, []*Node{call}, nil, 0)
	root.setHasError(true)

	normalizeKotlinCompatibility(root, source, lang)

	if root.HasError() {
		t.Fatalf("root HasError = true, want false:\n%s", root.SExpr(lang))
	}
	if got, want := call.ChildCount(), 2; got != want {
		t.Fatalf("call child count = %d, want %d:\n%s", got, want, root.SExpr(lang))
	}
	gotSuffix := call.Child(1)
	if got, want := gotSuffix.Type(lang), "call_suffix"; got != want {
		t.Fatalf("suffix type = %q, want %q:\n%s", got, want, root.SExpr(lang))
	}
	wantTypes := []string{"type_arguments", "value_arguments"}
	for i, want := range wantTypes {
		if got := gotSuffix.Child(i).Type(lang); got != want {
			t.Fatalf("suffix child[%d] type = %q, want %q:\n%s", i, got, want, root.SExpr(lang))
		}
	}
	if p := gotSuffix.Parent(); p != call {
		t.Fatal("rewritten suffix parent not updated")
	}
}

func TestNormalizeKotlinRecoveredNavigationGenericCallTypeArguments(t *testing.T) {
	lang := kotlinGenericCallTestLanguage()
	source := []byte(`Flux.fromIterable<Map.Entry<Int, LongWrapper>>(Iterable { map.entries.iterator() })`)
	arena := newNodeArena(arenaClassFull)
	base := newLeafNodeInArena(arena, 7, true, 0, 4, Point{}, Point{Column: 4})
	dot := newLeafNodeInArena(arena, 20, false, 4, 5, Point{Column: 4}, Point{Column: 5})
	name := newLeafNodeInArena(arena, 7, true, 5, 17, Point{Column: 5}, Point{Column: 17})
	navSuffix := newParentNodeInArena(arena, 6, true, []*Node{dot, name}, nil, 0)
	lt := newLeafNodeInArena(arena, 16, false, 17, 18, Point{Column: 17}, Point{Column: 18})
	typ := newLeafNodeInArena(arena, 8, true, 18, 45, Point{Column: 18}, Point{Column: 45})
	gt := newLeafNodeInArena(arena, 17, false, 45, 46, Point{Column: 45}, Point{Column: 46})
	typeArgs := newParentNodeInArena(arena, 9, true, []*Node{lt, typ, gt}, nil, 0)
	errNode := newParentNodeInArena(arena, errorSymbol, true, []*Node{navSuffix, typeArgs}, nil, 0)
	errNode.setHasError(true)
	valueArgs := newLeafNodeInArena(arena, 12, true, 46, 83, Point{Column: 46}, Point{Column: 83})
	suffix := newParentNodeInArena(arena, 4, true, []*Node{valueArgs}, nil, 0)
	call := newParentNodeInArena(arena, 3, true, []*Node{base, errNode, suffix}, nil, 0)
	call.setHasError(true)
	root := newParentNodeInArena(arena, 1, true, []*Node{call}, nil, 0)
	root.setHasError(true)

	normalizeKotlinCompatibility(root, source, lang)

	if root.HasError() {
		t.Fatalf("root HasError = true, want false:\n%s", root.SExpr(lang))
	}
	if got, want := call.ChildCount(), 2; got != want {
		t.Fatalf("call child count = %d, want %d:\n%s", got, want, root.SExpr(lang))
	}
	nav := call.Child(0)
	if got, want := nav.Type(lang), "navigation_expression"; got != want {
		t.Fatalf("call child[0] = %q, want %q:\n%s", got, want, root.SExpr(lang))
	}
	gotSuffix := call.Child(1)
	wantTypes := []string{"type_arguments", "value_arguments"}
	for i, want := range wantTypes {
		if got := gotSuffix.Child(i).Type(lang); got != want {
			t.Fatalf("suffix child[%d] type = %q, want %q:\n%s", i, got, want, root.SExpr(lang))
		}
	}
}

func kotlinPrefixComparisonTestLanguage() *Language {
	return &Language{
		Name: "kotlin",
		SymbolNames: []string{
			"EOF", "source_file", "prefix_expression", "comparison_expression",
			"simple_identifier", "++", "--", "<",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "prefix_expression", Visible: true, Named: true},
			{Name: "comparison_expression", Visible: true, Named: true},
			{Name: "simple_identifier", Visible: true, Named: true},
			{Name: "++", Visible: true, Named: false},
			{Name: "--", Visible: true, Named: false},
			{Name: "<", Visible: true, Named: false},
		},
	}
}

func TestNormalizeKotlinPrefixComparisonExpressions(t *testing.T) {
	lang := kotlinPrefixComparisonTestLanguage()
	source := []byte("++consumed < count")
	arena := newNodeArena(arenaClassFull)
	inc := newLeafNodeInArena(arena, 5, false, 0, 2, Point{}, Point{Column: 2})
	left := newLeafNodeInArena(arena, 4, true, 2, 10, Point{Column: 2}, Point{Column: 10})
	lt := newLeafNodeInArena(arena, 7, false, 11, 12, Point{Column: 11}, Point{Column: 12})
	right := newLeafNodeInArena(arena, 4, true, 13, 18, Point{Column: 13}, Point{Column: 18})
	cmp := newParentNodeInArena(arena, 3, true, []*Node{left, lt, right}, nil, 0)
	prefix := newParentNodeInArena(arena, 2, true, []*Node{inc, cmp}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{prefix}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := prefix.Type(lang), "comparison_expression"; got != want {
		t.Fatalf("node type = %q, want %q", got, want)
	}
	if got, want := prefix.ChildCount(), 3; got != want {
		t.Fatalf("child count = %d, want %d", got, want)
	}
	if got := prefix.Child(0).Type(lang); got != "prefix_expression" {
		t.Fatalf("left child type = %q, want prefix_expression", got)
	}
	if got := prefix.Child(0).Child(1).Text(source); got != "consumed" {
		t.Fatalf("prefix operand text = %q, want consumed", got)
	}
	if got := prefix.Child(2).Text(source); got != "count" {
		t.Fatalf("comparison right text = %q, want count", got)
	}
	if p := prefix.Child(0).Parent(); p != prefix {
		t.Fatal("rewritten prefix parent not updated")
	}
}

func kotlinCallableReferenceTestLanguage() *Language {
	return &Language{
		Name: "kotlin",
		SymbolNames: []string{
			"EOF", "source_file", "navigation_expression", "navigation_suffix",
			"simple_identifier", "callable_reference", "type_identifier", "::", "class",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "navigation_expression", Visible: true, Named: true},
			{Name: "navigation_suffix", Visible: true, Named: true},
			{Name: "simple_identifier", Visible: true, Named: true},
			{Name: "callable_reference", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
			{Name: "::", Visible: true, Named: false},
			{Name: "class", Visible: true, Named: false},
		},
	}
}

func TestNormalizeKotlinCallableReferenceNavigations(t *testing.T) {
	lang := kotlinCallableReferenceTestLanguage()
	// Mirrors `Exception::class`: C tree-sitter resolves the ambiguity to
	// callable_reference(type_identifier "::" class).
	source := []byte("Exception::class")
	arena := newNodeArena(arenaClassFull)
	base := newLeafNodeInArena(arena, 4, true, 0, 9, Point{}, Point{Column: 9})
	op := newLeafNodeInArena(arena, 7, false, 9, 11, Point{Column: 9}, Point{Column: 11})
	target := newLeafNodeInArena(arena, 8, false, 11, 16, Point{Column: 11}, Point{Column: 16})
	suffix := newParentNodeInArena(arena, 3, true, []*Node{op, target}, nil, 0)
	nav := newParentNodeInArena(arena, 2, true, []*Node{base, suffix}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{nav}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := nav.Type(lang), "callable_reference"; got != want {
		t.Fatalf("node type = %q, want %q", got, want)
	}
	if got, want := nav.ChildCount(), 3; got != want {
		t.Fatalf("child count = %d, want %d", got, want)
	}
	wantTypes := []string{"type_identifier", "::", "class"}
	for i, want := range wantTypes {
		if got := nav.Child(i).Type(lang); got != want {
			t.Fatalf("child[%d] type = %q, want %q", i, got, want)
		}
	}
	if p := nav.Child(0).Parent(); p != nav {
		t.Fatal("rewritten child parent not updated")
	}
}

func TestNormalizeKotlinCallableReferenceNavigationsSkipsChainedBase(t *testing.T) {
	lang := kotlinCallableReferenceTestLanguage()
	// Mirrors `a.b::c`: a chained base stays a navigation_expression in C.
	source := []byte("a.b::c")
	arena := newNodeArena(arenaClassFull)
	innerBase := newLeafNodeInArena(arena, 4, true, 0, 3, Point{}, Point{Column: 3})
	op := newLeafNodeInArena(arena, 7, false, 3, 5, Point{Column: 3}, Point{Column: 5})
	target := newLeafNodeInArena(arena, 4, true, 5, 6, Point{Column: 5}, Point{Column: 6})
	suffix := newParentNodeInArena(arena, 3, true, []*Node{op, target}, nil, 0)
	// Base is itself a navigation_expression, not a bare simple_identifier.
	baseNav := newParentNodeInArena(arena, 2, true, []*Node{innerBase}, nil, 0)
	nav := newParentNodeInArena(arena, 2, true, []*Node{baseNav, suffix}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{nav}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := nav.Type(lang), "navigation_expression"; got != want {
		t.Fatalf("node type = %q, want %q", got, want)
	}
	if got, want := nav.ChildCount(), 2; got != want {
		t.Fatalf("child count = %d, want %d", got, want)
	}
}

func kotlinReceiverTestLanguage() *Language {
	return &Language{
		Name: "kotlin",
		SymbolNames: []string{
			"EOF", "source_file", "function_declaration", "receiver_type",
			"user_type", "type_identifier", "simple_identifier", ".", "fun",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "function_declaration", Visible: true, Named: true},
			{Name: "receiver_type", Visible: true, Named: true},
			{Name: "user_type", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
			{Name: "simple_identifier", Visible: true, Named: true},
			{Name: ".", Visible: true, Named: false},
			{Name: "fun", Visible: true, Named: false},
		},
	}
}

func kotlinStringTestLanguage() *Language {
	return &Language{
		Name: "kotlin",
		SymbolNames: []string{
			"EOF", "source_file", "string_literal", "string_content",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "string_literal", Visible: true, Named: true},
			{Name: "string_content", Visible: true, Named: true},
		},
	}
}

func TestNormalizeKotlinRawStringTrailingContent(t *testing.T) {
	lang := kotlinStringTestLanguage()
	source := []byte("\"\"\"dispatcher = \"thread-pool-executor\"\nfixed-pool-size = 1\n\"\"\"")
	arena := newNodeArena(arenaClassFull)
	firstContent := newLeafNodeInArena(arena, 3, true, 3, 38, Point{Column: 3}, Point{Column: 38})
	str := newParentNodeInArena(arena, 2, true, []*Node{firstContent}, nil, 0)
	str.startByte = 0
	str.endByte = uint32(len(source))
	str.startPoint = Point{}
	str.endPoint = advancePointByBytes(Point{}, source)
	root := newParentNodeInArena(arena, 1, true, []*Node{str}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := str.ChildCount(), 2; got != want {
		t.Fatalf("string child count = %d, want %d", got, want)
	}
	trailing := str.Child(1)
	if got, want := trailing.Type(lang), "string_content"; got != want {
		t.Fatalf("trailing child type = %q, want %q", got, want)
	}
	if trailing.StartByte() != firstContent.EndByte() || trailing.EndByte() != uint32(len(source)-3) {
		t.Fatalf("trailing span = [%d:%d], want [%d:%d]",
			trailing.StartByte(), trailing.EndByte(), firstContent.EndByte(), len(source)-3)
	}
	if got, want := trailing.Text(source), "\nfixed-pool-size = 1\n"; got != want {
		t.Fatalf("trailing text = %q, want %q", got, want)
	}
	if p := trailing.Parent(); p != str {
		t.Fatal("trailing child parent not updated")
	}
}

func TestNormalizeKotlinRawStringTrailingContentSkipsInterpolation(t *testing.T) {
	lang := kotlinStringTestLanguage()
	source := []byte("\"\"\"before \"quote\"\n$value\n\"\"\"")
	arena := newNodeArena(arenaClassFull)
	firstContent := newLeafNodeInArena(arena, 3, true, 3, 17, Point{Column: 3}, Point{Column: 17})
	str := newParentNodeInArena(arena, 2, true, []*Node{firstContent}, nil, 0)
	str.startByte = 0
	str.endByte = uint32(len(source))
	str.startPoint = Point{}
	str.endPoint = advancePointByBytes(Point{}, source)
	root := newParentNodeInArena(arena, 1, true, []*Node{str}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := str.ChildCount(), 1; got != want {
		t.Fatalf("string child count = %d, want %d", got, want)
	}
}

func TestNormalizeKotlinReceiverFunctionNames(t *testing.T) {
	lang := kotlinReceiverTestLanguage()
	// Mirrors `fun A.B.f()` parsed with the dotted path swallowed by the
	// receiver and a zero-width simple_identifier where the name should be.
	source := []byte("fun A.B.f()")
	arena := newNodeArena(arenaClassFull)
	funTok := newLeafNodeInArena(arena, 8, false, 0, 3, Point{}, Point{Column: 3})
	tA := newLeafNodeInArena(arena, 5, true, 4, 5, Point{Column: 4}, Point{Column: 5})
	d1 := newLeafNodeInArena(arena, 7, false, 5, 6, Point{Column: 5}, Point{Column: 6})
	tB := newLeafNodeInArena(arena, 5, true, 6, 7, Point{Column: 6}, Point{Column: 7})
	d2 := newLeafNodeInArena(arena, 7, false, 7, 8, Point{Column: 7}, Point{Column: 8})
	tF := newLeafNodeInArena(arena, 5, true, 8, 9, Point{Column: 8}, Point{Column: 9})
	user := newParentNodeInArena(arena, 4, true, []*Node{tA, d1, tB, d2, tF}, nil, 0)
	recv := newParentNodeInArena(arena, 3, true, []*Node{user}, nil, 0)
	zeroName := newLeafNodeInArena(arena, 6, true, 9, 9, Point{Column: 9}, Point{Column: 9})
	fn := newParentNodeInArena(arena, 2, true, []*Node{funTok, recv, zeroName}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{fn}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := fn.ChildCount(), 4; got != want {
		t.Fatalf("function child count = %d, want %d", got, want)
	}
	wantTypes := []string{"fun", "receiver_type", ".", "simple_identifier"}
	for i, want := range wantTypes {
		if got := fn.Child(i).Type(lang); got != want {
			t.Fatalf("child[%d] type = %q, want %q", i, got, want)
		}
	}
	name := fn.Child(3)
	if name.StartByte() != 8 || name.EndByte() != 9 {
		t.Fatalf("name span = [%d:%d], want [8:9]", name.StartByte(), name.EndByte())
	}
	if recv.EndByte() != 7 || user.EndByte() != 7 {
		t.Fatalf("receiver/user end = %d/%d, want 7/7", recv.EndByte(), user.EndByte())
	}
	if got, want := user.ChildCount(), 3; got != want {
		t.Fatalf("user_type child count = %d, want %d", got, want)
	}
	if p := name.Parent(); p != fn {
		t.Fatal("name parent not updated")
	}
}

func TestNormalizeKotlinReceiverFunctionNamesSkipsRealNames(t *testing.T) {
	lang := kotlinReceiverTestLanguage()
	// `fun A.f()` already parsed correctly: a non-zero-width name must not be
	// rewritten.
	source := []byte("fun A.f()")
	arena := newNodeArena(arenaClassFull)
	funTok := newLeafNodeInArena(arena, 8, false, 0, 3, Point{}, Point{Column: 3})
	tA := newLeafNodeInArena(arena, 5, true, 4, 5, Point{Column: 4}, Point{Column: 5})
	user := newParentNodeInArena(arena, 4, true, []*Node{tA}, nil, 0)
	recv := newParentNodeInArena(arena, 3, true, []*Node{user}, nil, 0)
	dot := newLeafNodeInArena(arena, 7, false, 5, 6, Point{Column: 5}, Point{Column: 6})
	name := newLeafNodeInArena(arena, 6, true, 6, 7, Point{Column: 6}, Point{Column: 7})
	fn := newParentNodeInArena(arena, 2, true, []*Node{funTok, recv, dot, name}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{fn}, nil, 0)

	normalizeKotlinCompatibility(root, source, lang)

	if got, want := fn.ChildCount(), 4; got != want {
		t.Fatalf("function child count = %d, want %d", got, want)
	}
	if got, want := user.ChildCount(), 1; got != want {
		t.Fatalf("user_type child count = %d, want %d", got, want)
	}
}
