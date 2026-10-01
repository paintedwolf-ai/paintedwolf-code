package grammargen

import (
	"strings"
	"testing"
)

func TestGenerateHighlightQueriesSimpleExtension(t *testing.T) {
	base := GoGrammar()
	ext := ExtendGrammar("test_ext", base, func(g *Grammar) {
		g.Define("let_declaration", Seq(
			Str("let"),
			Field("name", Sym("identifier")),
			Str("="),
			Field("value", Sym("_expression")),
		))
		g.Define("enum_variant", Seq(
			Field("name", Sym("identifier")),
		))
		g.Define("enum_declaration", Seq(
			Str("enum"),
			Field("name", Sym("identifier")),
			Str("{"),
			CommaSep1(Sym("enum_variant")),
			Str("}"),
		))
		AppendChoice(g, "_statement", Sym("let_declaration"))
		AppendChoice(g, "_top_level_declaration", Sym("enum_declaration"))
		AddConflict(g, "_statement", "let_declaration")
	})

	queries := GenerateHighlightQueries(base, ext)
	t.Logf("Highlights:\n%s", queries)

	// Keywords
	if !strings.Contains(queries, `"let" @keyword`) {
		t.Error("expected let keyword highlight")
	}
	if !strings.Contains(queries, `"enum" @keyword`) {
		t.Error("expected enum keyword highlight")
	}

	// Operators
	// "=>" would show if we added it to the grammar

	// Rule-specific highlights
	if !strings.Contains(queries, "let_declaration") {
		t.Error("expected let_declaration highlight rule")
	}
	if !strings.Contains(queries, "@variable.definition") {
		t.Error("expected @variable.definition for let")
	}
	if !strings.Contains(queries, "enum_variant") {
		t.Error("expected enum_variant highlight")
	}
	if !strings.Contains(queries, "@constructor") {
		t.Error("expected @constructor for variants")
	}
	if !strings.Contains(queries, "enum_declaration") {
		t.Error("expected enum_declaration highlight")
	}
	if !strings.Contains(queries, "@type.definition") {
		t.Error("expected @type.definition for declarations")
	}
}

func TestGenerateHighlightQueriesNoNewRules(t *testing.T) {
	base := GoGrammar()
	queries := GenerateHighlightQueries(base, base)
	if queries != "" {
		t.Errorf("expected empty queries for identical grammars, got:\n%s", queries)
	}
}
