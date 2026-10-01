package grep

import (
	"testing"
)

func TestParseQuery_FullForm(t *testing.T) {
	q, err := ParseQuery(`find go::func $NAME($$$) error`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
	if q.Where != "" {
		t.Errorf("Where = %q, want empty", q.Where)
	}
	if q.Replace != "" {
		t.Errorf("Replace = %q, want empty", q.Replace)
	}
}

func TestParseQuery_ShorthandNoFind(t *testing.T) {
	q, err := ParseQuery(`go::func $NAME($$$) error`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
}

func TestParseQuery_BarePattern(t *testing.T) {
	q, err := ParseQuery(`func $NAME($$$) error`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "" {
		t.Errorf("Lang = %q, want empty", q.Lang)
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
}

func TestParseQuery_SexpMode(t *testing.T) {
	q, err := ParseQuery(`find sexp::(function_definition)`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "sexp" {
		t.Errorf("Lang = %q, want %q", q.Lang, "sexp")
	}
	if q.Pattern != "(function_definition)" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "(function_definition)")
	}
}

func TestParseQuery_WithWhere(t *testing.T) {
	q, err := ParseQuery(`find go::func $NAME($$$) error where { $NAME != "main" }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
	if q.Where != `$NAME != "main"` {
		t.Errorf("Where = %q, want %q", q.Where, `$NAME != "main"`)
	}
}

func TestParseQuery_WithReplace(t *testing.T) {
	q, err := ParseQuery(`find go::func $NAME($$$) error replace { func $NAME($$$) (error, bool) }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
	if q.Replace != "func $NAME($$$) (error, bool)" {
		t.Errorf("Replace = %q, want %q", q.Replace, "func $NAME($$$) (error, bool)")
	}
}

func TestParseQuery_WithWhereAndReplace(t *testing.T) {
	q, err := ParseQuery(`find go::func $NAME($$$) error where { $NAME != "main" } replace { func $NAME($$$) (error, bool) }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME($$$) error" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$) error")
	}
	if q.Where != `$NAME != "main"` {
		t.Errorf("Where = %q, want %q", q.Where, `$NAME != "main"`)
	}
	if q.Replace != "func $NAME($$$) (error, bool)" {
		t.Errorf("Replace = %q, want %q", q.Replace, "func $NAME($$$) (error, bool)")
	}
}

func TestParseQuery_NestedBracesInWhere(t *testing.T) {
	q, err := ParseQuery(`find go::$X where { $X matches { foo { bar } } }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Where != "$X matches { foo { bar } }" {
		t.Errorf("Where = %q, want %q", q.Where, "$X matches { foo { bar } }")
	}
}

func TestParseQuery_NestedBracesInReplace(t *testing.T) {
	q, err := ParseQuery(`find go::$X replace { if $X { return } }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Replace != "if $X { return }" {
		t.Errorf("Replace = %q, want %q", q.Replace, "if $X { return }")
	}
}

func TestParseQuery_EmptyQuery(t *testing.T) {
	_, err := ParseQuery("")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestParseQuery_WhitespaceOnlyQuery(t *testing.T) {
	_, err := ParseQuery("   \t\n  ")
	if err == nil {
		t.Fatal("expected error for whitespace-only query")
	}
}

func TestParseQuery_FindKeywordAlone(t *testing.T) {
	_, err := ParseQuery("find")
	if err == nil {
		t.Fatal("expected error for incomplete find query")
	}
}

func TestParseQuery_FindWithWhitespaceOnly(t *testing.T) {
	_, err := ParseQuery("find   ")
	if err == nil {
		t.Fatal("expected error for find with no pattern")
	}
}

func TestParseQuery_RustLanguage(t *testing.T) {
	q, err := ParseQuery(`rust::fn $NAME() -> Result<$T, $E>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "rust" {
		t.Errorf("Lang = %q, want %q", q.Lang, "rust")
	}
	if q.Pattern != "fn $NAME() -> Result<$T, $E>" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "fn $NAME() -> Result<$T, $E>")
	}
}

func TestParseQuery_PythonLanguage(t *testing.T) {
	q, err := ParseQuery(`find python::def $NAME($$$):`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "python" {
		t.Errorf("Lang = %q, want %q", q.Lang, "python")
	}
	if q.Pattern != "def $NAME($$$):" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "def $NAME($$$):")
	}
}

func TestParseQuery_WhereOnlyNoReplace(t *testing.T) {
	q, err := ParseQuery(`go::$X where { len($X) > 0 }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "$X" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "$X")
	}
	if q.Where != "len($X) > 0" {
		t.Errorf("Where = %q, want %q", q.Where, "len($X) > 0")
	}
	if q.Replace != "" {
		t.Errorf("Replace = %q, want empty", q.Replace)
	}
}

func TestParseQuery_ReplaceOnlyNoWhere(t *testing.T) {
	q, err := ParseQuery(`go::$X replace { $Y }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Pattern != "$X" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "$X")
	}
	if q.Where != "" {
		t.Errorf("Where = %q, want empty", q.Where)
	}
	if q.Replace != "$Y" {
		t.Errorf("Replace = %q, want %q", q.Replace, "$Y")
	}
}

func TestParseQuery_LeadingTrailingWhitespace(t *testing.T) {
	q, err := ParseQuery(`   find go::func $NAME()   `)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "func $NAME()" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME()")
	}
}

func TestParseQuery_UnmatchedBraceInWhere(t *testing.T) {
	_, err := ParseQuery(`find go::$X where { unmatched`)
	if err == nil {
		t.Fatal("expected error for unmatched brace in where block")
	}
}

func TestParseQuery_UnmatchedBraceInReplace(t *testing.T) {
	_, err := ParseQuery(`find go::$X replace { unmatched`)
	if err == nil {
		t.Fatal("expected error for unmatched brace in replace block")
	}
}

func TestParseQuery_EmptyWhereBlock(t *testing.T) {
	q, err := ParseQuery(`find go::$X where { } replace { $Y }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Where != "" {
		t.Errorf("Where = %q, want empty", q.Where)
	}
	if q.Replace != "$Y" {
		t.Errorf("Replace = %q, want %q", q.Replace, "$Y")
	}
}

func TestParseQuery_EmptyReplaceBlock(t *testing.T) {
	q, err := ParseQuery(`find go::$X where { $X > 0 } replace { }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Where != "$X > 0" {
		t.Errorf("Where = %q, want %q", q.Where, "$X > 0")
	}
	if q.Replace != "" {
		t.Errorf("Replace = %q, want empty", q.Replace)
	}
}

func TestParseQuery_BarePatternWithWhere(t *testing.T) {
	q, err := ParseQuery(`func $NAME($$$) where { $NAME != "init" }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "" {
		t.Errorf("Lang = %q, want empty", q.Lang)
	}
	if q.Pattern != "func $NAME($$$)" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "func $NAME($$$)")
	}
	if q.Where != `$NAME != "init"` {
		t.Errorf("Where = %q, want %q", q.Where, `$NAME != "init"`)
	}
}

func TestParseQuery_PatternContainingColons(t *testing.T) {
	// A bare pattern that contains :: but is not a lang prefix
	// e.g., "std::vector<$T>" with no lang prefix should try to parse
	// "std" as a language since it appears before ::
	q, err := ParseQuery(`cpp::std::vector<$T>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "cpp" {
		t.Errorf("Lang = %q, want %q", q.Lang, "cpp")
	}
	if q.Pattern != "std::vector<$T>" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "std::vector<$T>")
	}
}

func TestParseQuery_DeeplyNestedBraces(t *testing.T) {
	q, err := ParseQuery(`find go::$X where { $X matches { a { b { c } } } }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Where != "$X matches { a { b { c } } }" {
		t.Errorf("Where = %q, want %q", q.Where, "$X matches { a { b { c } } }")
	}
}

func TestParseQuery_MultipleColonsInPattern(t *testing.T) {
	q, err := ParseQuery(`find go::$X := map[$K]$V{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "go" {
		t.Errorf("Lang = %q, want %q", q.Lang, "go")
	}
	if q.Pattern != "$X := map[$K]$V{}" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "$X := map[$K]$V{}")
	}
}

func TestParseQuery_SexpWithWhere(t *testing.T) {
	q, err := ParseQuery(`sexp::(function_definition name: (identifier) @name) where { @name != "test" }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Lang != "sexp" {
		t.Errorf("Lang = %q, want %q", q.Lang, "sexp")
	}
	if q.Pattern != "(function_definition name: (identifier) @name)" {
		t.Errorf("Pattern = %q, want %q", q.Pattern, "(function_definition name: (identifier) @name)")
	}
	if q.Where != `@name != "test"` {
		t.Errorf("Where = %q, want %q", q.Where, `@name != "test"`)
	}
}

func TestParseQuery_WhereBlockMissingOpenBrace(t *testing.T) {
	_, err := ParseQuery(`find go::$X where $X > 0`)
	if err == nil {
		t.Fatal("expected error for where block missing opening brace")
	}
}

func TestParseQuery_ReplaceBlockMissingOpenBrace(t *testing.T) {
	_, err := ParseQuery(`find go::$X replace $Y`)
	if err == nil {
		t.Fatal("expected error for replace block missing opening brace")
	}
}

func TestParseQuery_ReplaceBeforeWhere(t *testing.T) {
	_, err := ParseQuery(`go::$X replace { $Y } where { $X > 0 }`)
	if err == nil {
		t.Fatal("expected error for replace before where")
	}
	want := "replace block must come after where block"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestParseQuery_TrailingAfterReplace(t *testing.T) {
	_, err := ParseQuery(`go::$X replace { $Y } this is garbage`)
	if err == nil {
		t.Fatal("expected error for trailing content after replace block")
	}
}

func TestQuery_String(t *testing.T) {
	tests := []struct {
		name  string
		query Query
		want  string
	}{
		{
			name:  "full form",
			query: Query{Lang: "go", Pattern: "func $NAME()", Where: `$NAME != "main"`, Replace: "func $NAME() error"},
			want:  `find go::func $NAME() where { $NAME != "main" } replace { func $NAME() error }`,
		},
		{
			name:  "no lang",
			query: Query{Pattern: "func $NAME()"},
			want:  `find func $NAME()`,
		},
		{
			name:  "with lang only",
			query: Query{Lang: "rust", Pattern: "fn $NAME()"},
			want:  `find rust::fn $NAME()`,
		},
		{
			name:  "where only",
			query: Query{Lang: "go", Pattern: "$X", Where: "$X > 0"},
			want:  `find go::$X where { $X > 0 }`,
		},
		{
			name:  "replace only",
			query: Query{Lang: "go", Pattern: "$X", Replace: "$Y"},
			want:  `find go::$X replace { $Y }`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.query.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
