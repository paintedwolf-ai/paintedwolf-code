package grammargen

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// These tests cover the alias-materialization bug fixed in
// enumerateAlternatives's RuleAlias case and substituteInlineRefsDepth
// (normalize.go), and the visible bare-string token/nonterminal
// classification fixed in isPlainVisibleNamedLeafStringLiteral
// (normalize.go).
//
// The underlying tree-sitter C compiler rules, confirmed empirically against
// tree-sitter CLI 0.26.6 and cross-checked against the real, pinned SQL
// (order/nulls_order) and CSS (!important) grammars:
//
//   - A direct alias-of-alias chain with nothing else in between collapses:
//     alias(alias(x, "a"), "b") == alias(x, "b") — the outermost name wins,
//     any intermediate name is discarded. The same collapse applies when the
//     inner alias() is reachable through nothing but alias-metadata
//     wrappers — prec/prec_left/prec_right/prec_dynamic/field — which carry
//     no naming of their own: alias(prec(N, alias(x, "a")), "b") ==
//     prec(N, alias(x, "b")), and likewise for field(). This does NOT apply
//     once a seq/choice/repeat/optional sits in between; those introduce
//     separate production steps, so the per-step rule below applies instead.
//   - An alias() wrapping a choice/seq collapses PER PRODUCTION STEP: a step
//     that is already aliased by an explicit nested alias() reachable
//     through that choice/seq structure keeps its own (inner) alias, and
//     the outer alias is dropped for that step entirely (not merged). A
//     step reached with no alias of its own — including a bare symbol
//     reference to another (possibly hidden) rule, whose own internal
//     aliasing lives on its own productions and isn't visible at this
//     step — still receives the outer alias normally. grammargen's own
//     SetInline mechanism (unlike real tree-sitter) splices a hidden rule's
//     body into every call site before alias resolution runs, so
//     substituteInlineRefsDepth strips any aliases that inlining exposes
//     under an enclosing alias, keeping inlined content as transparent to
//     alias resolution as an un-inlined symbol reference would have been.
//   - A visible (non-hidden) named rule whose entire body is a single bare
//     string literal collapses into a plain named terminal (no wrapper
//     node) based on ownership, not shape — unique owner of that exact
//     string value, and the value never used bare elsewhere in the grammar.
//     isPlainVisibleNamedLeafStringLiteral only special-cased pure
//     punctuation and lowercase-identifier-like shapes; it now also
//     recognizes a run of punctuation immediately followed by a
//     lowercase-identifier-like tail (CSS's "!important"), without widening
//     the bar for other shapes (a rule differing from an identifier only by
//     case, like "True", still keeps its wrapper).
//
// The `ident` helper rule in these grammars matches capitalized identifiers
// only ([A-Z][a-zA-Z0-9]*) so it can never collide with the lowercase
// keyword-shaped literals under test (asc/desc/nulls/first/to/etc).

var testWhitespaceExtras = []*Rule{Pat(`[ \t]+`)}

func mustGenerate(t *testing.T, g *Grammar) *gotreesitter.Language {
	t.Helper()
	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	return lang
}

func mustParseClean(t *testing.T, lang *gotreesitter.Language, src string) *gotreesitter.Node {
	t.Helper()
	tree, err := gotreesitter.NewParser(lang).Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("parse %q produced ERROR: %s", src, root.SExpr(lang))
	}
	return root
}

func collectNodeTypes(node *gotreesitter.Node, lang *gotreesitter.Language, out map[string]int) {
	if node == nil {
		return
	}
	out[node.Type(lang)]++
	for i := 0; i < node.ChildCount(); i++ {
		collectNodeTypes(node.Child(i), lang, out)
	}
}

// TestAliasOfChoiceWithAlreadyAliasedArmsDropsOuterAlias reproduces the SQL
// grammar bug: alias(choice(alias(A, "ASC"), alias(B, "DESC")), $.order).
// tree-sitter's C compiler never materializes an "order" wrapper here —
// both choice arms already name themselves, so the outer alias has nothing
// left to attach to.
func TestAliasOfChoiceWithAlreadyAliasedArmsDropsOuterAlias(t *testing.T) {
	g := NewGrammar("test_alias_choice_dedup")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Choice(
			Alias(Pat("[Aa][Ss][Cc]"), "ASC", false),
			Alias(Pat("[Dd][Ee][Ss][Cc]"), "DESC", false),
		), "order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	for _, src := range []string{"Foo asc", "Foo desc"} {
		root := mustParseClean(t, lang, src)
		types := map[string]int{}
		collectNodeTypes(root, lang, types)
		if types["order"] != 0 {
			t.Fatalf("parse %q: spurious \"order\" wrapper node materialized: %s", src, root.SExpr(lang))
		}
	}
}

// TestAliasOfSeqWithAllStepsAlreadyAliasedDropsOuterAlias reproduces the SQL
// grammar's nulls_order bug: alias(seq(alias(A,"NULLS"),
// choice(alias(B,"FIRST"), alias(C,"LAST"))), $.nulls_order). Every step
// under the seq already names itself, so the outer alias is dropped
// entirely — not even applied to one of the steps.
func TestAliasOfSeqWithAllStepsAlreadyAliasedDropsOuterAlias(t *testing.T) {
	g := NewGrammar("test_alias_seq_dedup")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Seq(
			Alias(Pat("[Nn][Uu][Ll][Ll][Ss]"), "NULLS", false),
			Choice(
				Alias(Pat("[Ff][Ii][Rr][Ss][Tt]"), "FIRST", false),
				Alias(Pat("[Ll][Aa][Ss][Tt]"), "LAST", false),
			),
		), "nulls_order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "Foo nulls first")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["nulls_order"] != 0 {
		t.Fatalf("spurious \"nulls_order\" wrapper node materialized: %s", root.SExpr(lang))
	}
	if types["NULLS"] != 1 || types["FIRST"] != 1 {
		t.Fatalf("expected raw NULLS/FIRST anonymous tokens as direct siblings: %s", root.SExpr(lang))
	}
}

// TestAliasOfMixedChoiceAppliesOuterOnlyToUnaliasedArm covers a choice where
// only one arm is already aliased: that arm keeps its own (inner) alias,
// while the other, unaliased arm still receives the outer alias normally.
func TestAliasOfMixedChoiceAppliesOuterOnlyToUnaliasedArm(t *testing.T) {
	g := NewGrammar("test_alias_choice_mixed")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Choice(
			Alias(Pat("[Aa][Ss][Cc]"), "ASC", false),
			Pat("[Dd][Ee][Ss][Cc]"),
		), "order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)

	ascRoot := mustParseClean(t, lang, "Foo asc")
	ascTypes := map[string]int{}
	collectNodeTypes(ascRoot, lang, ascTypes)
	if ascTypes["order"] != 0 {
		t.Fatalf("ASC arm: spurious \"order\" wrapper (inner alias should win): %s", ascRoot.SExpr(lang))
	}
	if ascTypes["ASC"] != 1 {
		t.Fatalf("ASC arm: expected raw ASC token: %s", ascRoot.SExpr(lang))
	}

	descRoot := mustParseClean(t, lang, "Foo desc")
	descTypes := map[string]int{}
	collectNodeTypes(descRoot, lang, descTypes)
	if descTypes["order"] != 1 {
		t.Fatalf("DESC arm: expected outer \"order\" alias to apply to the unaliased arm: %s", descRoot.SExpr(lang))
	}
}

// TestDirectAliasChainCollapsesToOutermostName covers a direct alias-of-alias
// chain with nothing else in between: alias(alias(x, "a"), "b") == alias(x,
// "b"). The outer name wins outright and the inner name is fully discarded
// — unlike the choice/seq-mediated cases above.
func TestDirectAliasChainCollapsesToOutermostName(t *testing.T) {
	g := NewGrammar("test_alias_direct_chain")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Alias(Pat("[Aa][Ss][Cc]"), "ASC", true), "order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "Foo asc")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["ASC"] != 0 {
		t.Fatalf("expected inner \"ASC\" alias to be fully discarded: %s", root.SExpr(lang))
	}
	if types["order"] != 1 {
		t.Fatalf("expected outermost \"order\" alias to win: %s", root.SExpr(lang))
	}
}

// TestAliasChainThroughPrecCollapsesToOutermostName covers a
// metadata-mediated alias chain: alias(prec(N, alias(x, "ASC")), "order").
// Unlike the choice/seq-mediated cases above, a prec/prec_left/prec_right/
// prec_dynamic wrapper carries no naming of its own and never introduces a
// separate production step -- tree-sitter's compiler merges it together
// with any alias() it wraps exactly like the direct alias-of-alias chain,
// so the OUTERMOST alias wins here too, through the prec wrapper.
func TestAliasChainThroughPrecCollapsesToOutermostName(t *testing.T) {
	g := NewGrammar("test_alias_chain_through_prec")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Prec(1, Alias(Pat("[Aa][Ss][Cc]"), "ASC", true)), "order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "Foo asc")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["ASC"] != 0 {
		t.Fatalf("expected inner \"ASC\" alias to be fully discarded through the prec wrapper: %s", root.SExpr(lang))
	}
	if types["order"] != 1 {
		t.Fatalf("expected outermost \"order\" alias to win through the prec wrapper: %s", root.SExpr(lang))
	}
}

// TestAliasChainThroughFieldCollapsesToOutermostName is the field-wrapper
// analogue of TestAliasChainThroughPrecCollapsesToOutermostName:
// alias(field("f", alias(x, "ASC")), "order") also collapses to the
// outermost alias, through the field wrapper.
func TestAliasChainThroughFieldCollapsesToOutermostName(t *testing.T) {
	g := NewGrammar("test_alias_chain_through_field")
	g.Define("source_file", Seq(
		Sym("ident"),
		Optional(Alias(Field("f", Alias(Pat("[Aa][Ss][Cc]"), "ASC", true)), "order", true)),
	))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "Foo asc")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["ASC"] != 0 {
		t.Fatalf("expected inner \"ASC\" alias to be fully discarded through the field wrapper: %s", root.SExpr(lang))
	}
	if types["order"] != 1 {
		t.Fatalf("expected outermost \"order\" alias to win through the field wrapper: %s", root.SExpr(lang))
	}
}

// TestAliasOfHiddenSymbolReferenceStillAppliesUniformly guards the
// pre-existing case that originally motivated grammargen's (overly broad)
// "outer always wins" behavior: alias($._hidden, "outer_name") where
// _hidden is a bare rule/symbol reference (not an inline alias()) whose own
// body independently aliases each of its choice arms, and _hidden is NOT
// listed in Grammar.Inline (so it stays a genuine separate nonterminal
// symbol, exactly like real tree-sitter's hidden rules — see
// TestAliasOfSetInlineHiddenSymbolReferenceStillAppliesUniformly below for
// the SetInline-specific variant of this same guarantee). Because content
// here is a plain Symbol — not a Choice/Seq/Alias reachable through this
// alias()'s own body — flattening treats it as a single untagged step, so
// the outer alias must still apply uniformly regardless of which of
// _hidden's own arms matched.
func TestAliasOfHiddenSymbolReferenceStillAppliesUniformly(t *testing.T) {
	g := NewGrammar("test_alias_hidden_symbol_ref")
	g.Define("source_file", Repeat1(Alias(Sym("_hidden"), "outer_name", true)))
	g.Define("_hidden", Choice(
		Alias(Str("a"), "inner_a", true),
		Alias(Str("b"), "inner_b", true),
	))

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "ab")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["outer_name"] != 2 {
		t.Fatalf("expected outer_name alias to apply uniformly to both arms of the hidden symbol reference: %s", root.SExpr(lang))
	}
}

// TestAliasOfSetInlineHiddenSymbolReferenceStillAppliesUniformly reproduces
// the real-world regression this fix introduced (and then fixed) via
// grammargen's own SetInline mechanism, mirroring JavaScript/TSX's actual
// alias(_jsx_identifier, $.property_identifier) idiom exactly:
//
//	_jsx_identifier: choice(alias(jsx_identifier, "identifier"), identifier)
//	_jsx_attribute_name: alias(_jsx_identifier, "property_identifier")
//
// Unlike TestAliasOfHiddenSymbolReferenceStillAppliesUniformly above,
// "_hidden" here IS listed in Grammar.Inline, so
// substituteInlineRefsDepth splices _hidden's own body — a choice with one
// already-aliased arm — directly into the alias()'s content BEFORE
// enumerateAlternatives ever runs. Structurally that looks exactly like a
// hand-written alias(choice(alias(...), ...), outer) — which
// TestAliasOfMixedChoiceAppliesOuterOnlyToUnaliasedArm establishes must let
// the inner alias win on its arm. But real tree-sitter never inlines hidden
// rule bodies at this stage (hidden nonterminals stay distinct grammar
// symbols all the way through table construction), so this is purely a
// grammargen implementation artifact — the outer alias must still apply
// uniformly to both arms, regardless of which one the hidden rule's own
// choice matched. This is what stripAliases (in substituteInlineRefsDepth)
// restores.
func TestAliasOfSetInlineHiddenSymbolReferenceStillAppliesUniformly(t *testing.T) {
	g := NewGrammar("test_alias_setinline_hidden_symbol_ref")
	g.Define("source_file", Repeat1(Alias(Sym("_hidden"), "outer_name", true)))
	g.Define("_hidden", Choice(
		Alias(Str("a"), "inner_a", true),
		Str("b"),
	))
	g.SetInline("_hidden")

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "ab")
	types := map[string]int{}
	collectNodeTypes(root, lang, types)
	if types["inner_a"] != 0 {
		t.Fatalf("hidden rule's own inner alias leaked through a SetInline substitution under an outer alias: %s", root.SExpr(lang))
	}
	if types["outer_name"] != 2 {
		t.Fatalf("expected outer_name alias to apply uniformly to both arms of the SetInline'd hidden symbol reference: %s", root.SExpr(lang))
	}
}

// TestVisibleBareStringRuleCollapsesRegardlessOfShape reproduces CSS's
// `important: $ => '!important'` bug: a visible named rule whose body is a
// single, uniquely-owned bare string literal collapses into a plain named
// leaf terminal (0 children) regardless of whether the string's shape is
// pure punctuation, a lowercase identifier, or — like "!important" — a
// punctuation-prefixed, identifier-like mix that falls outside both
// buckets.
func TestVisibleBareStringRuleCollapsesRegardlessOfShape(t *testing.T) {
	g := NewGrammar("test_visible_bare_string_mixed_shape")
	g.Define("source_file", Seq(Sym("ident"), Optional(Sym("important"))))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Define("important", Str("!important"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "X !important")

	var importantNode *gotreesitter.Node
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "important" {
			importantNode = n
		}
		for i := 0; i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	if importantNode == nil {
		t.Fatalf("no \"important\" node found: %s", root.SExpr(lang))
	}
	if importantNode.ChildCount() != 0 {
		t.Fatalf("\"important\" node = %d children, want 0 (plain named leaf, not a wrapper): %s", importantNode.ChildCount(), root.SExpr(lang))
	}
}

// TestVisibleBareStringRuleSharedWithBareLiteralStaysWrapped is a regression
// guard for the pre-existing, still-required half of the classification
// rule: when the exact same string value is ALSO used bare (unaliased,
// un-owned) elsewhere in the grammar, the named rule must keep its
// nonterminal wrapper — collapsing it would incorrectly rename the other,
// unrelated bare occurrence too. This must hold regardless of the shape
// widening above.
func TestVisibleBareStringRuleSharedWithBareLiteralStaysWrapped(t *testing.T) {
	g := NewGrammar("test_visible_bare_string_shared")
	g.Define("source_file", Seq(Sym("to"), Sym("ident"), Optional(Seq(Str("to"), Str("("), Sym("ident"), Str(")")))))
	g.Define("to", Str("to"))
	g.Define("ident", Pat("[A-Z][a-zA-Z0-9]*"))
	g.Extras = testWhitespaceExtras

	lang := mustGenerate(t, g)
	root := mustParseClean(t, lang, "to Foo to ( Bar )")

	var toNode *gotreesitter.Node
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "to" && n.IsNamed() && toNode == nil {
			toNode = n
		}
		for i := 0; i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	if toNode == nil {
		t.Fatalf("no named \"to\" node found: %s", root.SExpr(lang))
	}
	if toNode.ChildCount() != 1 {
		t.Fatalf("named \"to\" node = %d children, want 1 (must stay wrapped: the bare literal \"to\" used elsewhere needs to stay anonymous): %s", toNode.ChildCount(), root.SExpr(lang))
	}
}
