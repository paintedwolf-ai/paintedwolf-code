package gotreesitter

import "testing"

func TestNormalizeTemplCompatibilityMergesComponentImportArgs(t *testing.T) {
	lang := &Language{
		Name:        "templ",
		SymbolNames: []string{"EOF", "source_file", "element", "component_import", "element_text", "argument_list", "(", ")", ",", "identifier", "@", "component_identifier"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "element", Visible: true, Named: true},
			{Name: "component_import", Visible: true, Named: true},
			{Name: "element_text", Visible: true, Named: true},
			{Name: "argument_list", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: ",", Visible: true, Named: false},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "@", Visible: true, Named: false},
			{Name: "component_identifier", Visible: true, Named: true},
		},
	}
	source := []byte("@counts(global, user)")
	arena := newNodeArena(arenaClassFull)
	atNode := newLeafNodeInArena(arena, 10, false, 0, 1, Point{}, Point{Column: 1})
	nameNode := newLeafNodeInArena(arena, 11, true, 1, 7, Point{Column: 1}, Point{Column: 7})
	importNode := newParentNodeInArena(arena, 3, true, []*Node{atNode, nameNode}, nil, 0)
	argsNode := newLeafNodeInArena(arena, 4, true, 7, uint32(len(source)), Point{Column: 7}, Point{Column: uint32(len(source))})
	root := newParentNodeInArena(arena, 2, true, []*Node{importNode, argsNode}, nil, 0)

	normalizeTemplCompatibility(root, source, lang)

	if got, want := root.ChildCount(), 1; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	child := root.Child(0)
	if child == nil {
		t.Fatal("merged child = nil")
	}
	if got, want := child.Type(lang), "component_import"; got != want {
		t.Fatalf("merged child type = %q, want %q", got, want)
	}
	if got, want := child.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("merged child end byte = %d, want %d", got, want)
	}
	if got, want := child.ChildCount(), 3; got != want {
		t.Fatalf("component_import child count = %d, want %d", got, want)
	}
	argList := child.Child(2)
	if argList == nil {
		t.Fatal("argument_list child = nil")
	}
	if got, want := argList.Type(lang), "argument_list"; got != want {
		t.Fatalf("argument child type = %q, want %q", got, want)
	}
	if got, want := argList.ChildCount(), 5; got != want {
		t.Fatalf("argument_list child count = %d, want %d", got, want)
	}
}

func TestNormalizeTemplCompatibilitySkipsNonParenthesizedElementText(t *testing.T) {
	lang := &Language{
		Name:        "templ",
		SymbolNames: []string{"EOF", "source_file", "element", "component_import", "element_text", "argument_list", "(", ")", ",", "identifier", "@", "component_identifier"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "element", Visible: true, Named: true},
			{Name: "component_import", Visible: true, Named: true},
			{Name: "element_text", Visible: true, Named: true},
			{Name: "argument_list", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: ",", Visible: true, Named: false},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "@", Visible: true, Named: false},
			{Name: "component_identifier", Visible: true, Named: true},
		},
	}
	source := []byte("@counts text")
	arena := newNodeArena(arenaClassFull)
	atNode := newLeafNodeInArena(arena, 10, false, 0, 1, Point{}, Point{Column: 1})
	nameNode := newLeafNodeInArena(arena, 11, true, 1, 7, Point{Column: 1}, Point{Column: 7})
	importNode := newParentNodeInArena(arena, 3, true, []*Node{atNode, nameNode}, nil, 0)
	textNode := newLeafNodeInArena(arena, 4, true, 7, uint32(len(source)), Point{Column: 7}, Point{Column: uint32(len(source))})
	root := newParentNodeInArena(arena, 2, true, []*Node{importNode, textNode}, nil, 0)

	normalizeTemplCompatibility(root, source, lang)

	if got, want := root.ChildCount(), 2; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	if got, want := importNode.EndByte(), uint32(7); got != want {
		t.Fatalf("component_import end byte = %d, want %d", got, want)
	}
}

func TestNormalizeTemplCompatibilityBuildsSimpleStringArgument(t *testing.T) {
	lang := templCompatibilityTestLanguage()
	source := []byte("@headerComponent(\"My Blog\")")
	arena := newNodeArena(arenaClassFull)
	atNode := newLeafNodeInArena(arena, 10, false, 0, 1, Point{}, Point{Column: 1})
	nameNode := newLeafNodeInArena(arena, 11, true, 1, 16, Point{Column: 1}, Point{Column: 16})
	importNode := newParentNodeInArena(arena, 3, true, []*Node{atNode, nameNode}, nil, 0)
	argsNode := newLeafNodeInArena(arena, 4, true, 16, uint32(len(source)), Point{Column: 16}, Point{Column: uint32(len(source))})
	root := newParentNodeInArena(arena, 2, true, []*Node{importNode, argsNode}, nil, 0)

	normalizeTemplCompatibility(root, source, lang)

	argList := root.Child(0).Child(2)
	if argList == nil {
		t.Fatal("argument_list child = nil")
	}
	if got, want := argList.ChildCount(), 3; got != want {
		t.Fatalf("argument_list child count = %d, want %d", got, want)
	}
	str := argList.Child(1)
	if str == nil {
		t.Fatal("string argument = nil")
	}
	if got, want := str.Type(lang), "interpreted_string_literal"; got != want {
		t.Fatalf("string argument type = %q, want %q", got, want)
	}
	if got, want := str.ChildCount(), 3; got != want {
		t.Fatalf("string child count = %d, want %d", got, want)
	}
}

func TestNormalizeTemplCompatibilityMergesQualifiedComponentImport(t *testing.T) {
	lang := templCompatibilityTestLanguage()
	source := []byte(`@templ.JSONScript("scriptData", scriptData)`)
	arena := newNodeArena(arenaClassFull)
	atNode := newLeafNodeInArena(arena, 10, false, 0, 1, Point{}, Point{Column: 1})
	nameNode := newLeafNodeInArena(arena, 11, true, 1, 6, Point{Column: 1}, Point{Column: 6})
	importNode := newParentNodeInArena(arena, 3, true, []*Node{atNode, nameNode}, nil, 0)
	tailNode := newLeafNodeInArena(arena, 4, true, 6, uint32(len(source)), Point{Column: 6}, Point{Column: uint32(len(source))})
	root := newParentNodeInArena(arena, 2, true, []*Node{importNode, tailNode}, nil, 0)

	normalizeTemplCompatibility(root, source, lang)

	if got, want := root.ChildCount(), 1; got != want {
		t.Fatalf("root child count = %d, want %d", got, want)
	}
	child := root.Child(0)
	if child == nil {
		t.Fatal("merged child = nil")
	}
	if got, want := child.Type(lang), "component_import"; got != want {
		t.Fatalf("merged child type = %q, want %q", got, want)
	}
	if got, want := child.ChildCount(), 5; got != want {
		t.Fatalf("component_import child count = %d, want %d", got, want)
	}
	checks := []struct {
		idx   int
		typ   string
		field string
		start uint32
		end   uint32
	}{
		{0, "@", "", 0, 1},
		{1, "package_identifier", "package", 1, 6},
		{2, ".", "", 6, 7},
		{3, "component_identifier", "name", 7, 17},
		{4, "argument_list", "arguments", 17, uint32(len(source))},
	}
	for _, check := range checks {
		got := child.Child(check.idx)
		if got == nil {
			t.Fatalf("child %d = nil", check.idx)
		}
		if typ := got.Type(lang); typ != check.typ {
			t.Fatalf("child %d type = %q, want %q", check.idx, typ, check.typ)
		}
		if field := child.FieldNameForChild(check.idx, lang); field != check.field {
			t.Fatalf("child %d field = %q, want %q", check.idx, field, check.field)
		}
		if start, end := got.StartByte(), got.EndByte(); start != check.start || end != check.end {
			t.Fatalf("child %d span = %d:%d, want %d:%d", check.idx, start, end, check.start, check.end)
		}
	}
}

func TestNormalizeTemplCompatibilityAddsDanglingAttributeQuoteError(t *testing.T) {
	lang := &Language{
		Name:        "templ",
		SymbolNames: []string{"EOF", "source_file", "tag_start", "<", "element_identifier", "attribute", ">", "\""},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "tag_start", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: "element_identifier", Visible: true, Named: true},
			{Name: "attribute", Visible: true, Named: true},
			{Name: ">", Visible: true, Named: false},
			{Name: "\"", Visible: true, Named: false},
		},
	}
	source := []byte("<a class=\"external-link\"\n\t>")
	quote := uint32(23)
	arena := newNodeArena(arenaClassFull)
	open := newLeafNodeInArena(arena, 3, false, 0, 1, Point{}, Point{Column: 1})
	name := newLeafNodeInArena(arena, 4, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	attr := newLeafNodeInArena(arena, 5, true, 3, quote, Point{Column: 3}, Point{Column: quote})
	close := newLeafNodeInArena(arena, 6, false, 26, 27, Point{Row: 1, Column: 1}, Point{Row: 1, Column: 2})
	tag := newParentNodeInArena(arena, 2, true, []*Node{open, name, attr, close}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{tag}, nil, 0)

	normalizeTemplCompatibility(root, source, lang)

	if got, want := tag.ChildCount(), 5; got != want {
		t.Fatalf("tag_start child count = %d, want %d; tree=%s", got, want, root.SExpr(lang))
	}
	err := tag.Child(3)
	if err == nil {
		t.Fatal("inserted error child = nil")
	}
	if got, want := err.Type(lang), "ERROR"; got != want {
		t.Fatalf("inserted child type = %q, want %q", got, want)
	}
	if start, end := err.StartByte(), err.EndByte(); start != quote || end != quote+1 {
		t.Fatalf("ERROR span = %d:%d, want %d:%d", start, end, quote, quote+1)
	}
	if got, want := err.ChildCount(), 1; got != want {
		t.Fatalf("ERROR child count = %d, want %d", got, want)
	}
	if child := err.Child(0); child == nil || child.Type(lang) != "\"" {
		t.Fatalf("ERROR child = %v, want quote token", child)
	}
	if !tag.HasError() || !root.HasError() {
		t.Fatalf("HasError not propagated: tag=%v root=%v", tag.HasError(), root.HasError())
	}
}

func templCompatibilityTestLanguage() *Language {
	return &Language{
		Name: "templ",
		SymbolNames: []string{
			"EOF", "source_file", "element", "component_import", "element_text",
			"argument_list", "(", ")", ",", "identifier", "@", "component_identifier",
			"interpreted_string_literal", "interpreted_string_literal_content", "\"",
			".", "package_identifier",
		},
		FieldNames: []string{"", "package", "name", "arguments"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "element", Visible: true, Named: true},
			{Name: "component_import", Visible: true, Named: true},
			{Name: "element_text", Visible: true, Named: true},
			{Name: "argument_list", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: ",", Visible: true, Named: false},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "@", Visible: true, Named: false},
			{Name: "component_identifier", Visible: true, Named: true},
			{Name: "interpreted_string_literal", Visible: true, Named: true},
			{Name: "interpreted_string_literal_content", Visible: true, Named: true},
			{Name: "\"", Visible: true, Named: false},
			{Name: ".", Visible: true, Named: false},
			{Name: "package_identifier", Visible: true, Named: true},
		},
	}
}
