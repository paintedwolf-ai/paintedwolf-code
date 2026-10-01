package grammargen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestNamedStringChoiceTokenBecomesKeyword(t *testing.T) {
	g := NewGrammar("named_string_choice_keyword")
	g.Define("source_file", Sym("predefined_type"))
	g.Define("predefined_type", Token(Choice(
		Str("int"),
		Str("string"),
		Str("nint"),
	)))
	g.Define("identifier", Pat(`[A-Za-z_][A-Za-z0-9_]*`))
	g.SetWord("identifier")

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	predefinedTypeSym := -1
	for i, sym := range ng.Symbols {
		if sym.Name == "predefined_type" {
			predefinedTypeSym = i
			break
		}
	}
	if predefinedTypeSym < 0 {
		t.Fatal("predefined_type symbol not found")
	}

	foundKeyword := false
	for _, symID := range ng.KeywordSymbols {
		if symID == predefinedTypeSym {
			foundKeyword = true
			break
		}
	}
	if !foundKeyword {
		t.Fatalf("predefined_type sym %d missing from keyword set", predefinedTypeSym)
	}

	for _, term := range ng.Terminals {
		if term.SymbolID == predefinedTypeSym {
			t.Fatalf("predefined_type sym %d still present in main terminals", predefinedTypeSym)
		}
	}

	foundEntry := false
	for _, entry := range ng.KeywordEntries {
		if entry.SymbolID == predefinedTypeSym {
			foundEntry = true
			break
		}
	}
	if !foundEntry {
		t.Fatalf("predefined_type sym %d missing from keyword entries", predefinedTypeSym)
	}
}

func TestBareLexicalChoiceBecomesNamedToken(t *testing.T) {
	g := NewGrammar("bare_lexical_choice_named_token")
	g.Define("source_file", Sym("builtin_type"))
	g.Define("builtin_type", Choice(
		Str("bool"),
		Pat(`(i|u)[1-9][0-9]*`),
	))
	g.Define("identifier", Pat(`[A-Za-z_][A-Za-z0-9_]*`))
	g.SetWord("identifier")

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	builtinTypeSym := -1
	for i, sym := range ng.Symbols {
		if sym.Name == "builtin_type" {
			builtinTypeSym = i
			if sym.Kind != SymbolNamedToken {
				t.Fatalf("builtin_type kind = %v, want SymbolNamedToken", sym.Kind)
			}
			break
		}
	}
	if builtinTypeSym < 0 {
		t.Fatal("builtin_type symbol not found")
	}
	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("i32"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	if got := tree.RootNode().SExpr(lang); got != "(source_file (builtin_type))" {
		t.Fatalf("SExpr = %s, want (source_file (builtin_type))", got)
	}
}

func TestExplicitPrecStringChoiceBecomesNonterminal(t *testing.T) {
	g := NewGrammar("explicit_prec_string_choice_nonterminal")
	g.Define("source_file", Sym("initializer"))
	g.Define("initializer", Seq(Sym("init_class"), Sym("identifier")))
	g.Define("init_class", PrecRight(0, Choice(Str("="), Str("+="), Str("-="))))
	g.Define("identifier", Pat(`[a-z]+`))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	initClassSym := -1
	for i, sym := range ng.Symbols {
		if sym.Name == "init_class" {
			initClassSym = i
			if sym.Kind != SymbolNonterminal {
				t.Fatalf("init_class kind = %v, want SymbolNonterminal", sym.Kind)
			}
			if !sym.Visible || !sym.Named {
				t.Fatalf("init_class metadata = visible:%v named:%v, want visible/named", sym.Visible, sym.Named)
			}
			break
		}
	}
	if initClassSym < 0 {
		t.Fatal("init_class symbol not found")
	}

	for _, term := range ng.Terminals {
		if term.SymbolID == initClassSym {
			t.Fatalf("init_class sym %d was emitted as a terminal", initClassSym)
		}
	}

	wantAlts := map[string]bool{"=": false, "+=": false, "-=": false}
	for _, prod := range ng.Productions {
		if prod.LHS != initClassSym || len(prod.RHS) != 1 {
			continue
		}
		rhs := prod.RHS[0]
		if rhs < 0 || rhs >= len(ng.Symbols) {
			continue
		}
		name := ng.Symbols[rhs].Name
		if _, ok := wantAlts[name]; ok {
			wantAlts[name] = true
		}
	}
	for name, found := range wantAlts {
		if !found {
			t.Fatalf("missing init_class -> %q production", name)
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("+=abc"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
	if got := root.SExpr(lang); got != "(source_file (initializer (init_class) (identifier)))" {
		t.Fatalf("SExpr = %s, want init_class wrapper", got)
	}
}

func TestPlainVisibleStringRuleBecomesNamedLeafToken(t *testing.T) {
	g := NewGrammar("plain_visible_string_rule_named_leaf_token")
	g.Define("source_file", Sym("null_literal"))
	g.Define("null_literal", Str("null"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "null_literal"); got != SymbolNamedToken {
		t.Fatalf("null_literal kind = %v, want SymbolNamedToken", got)
	}
	for _, sym := range ng.Symbols {
		if sym.Name == "null" && sym.Kind == SymbolTerminal {
			t.Fatalf("plain named string rule also registered anonymous %q terminal", sym.Name)
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("null"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	lit := root.Child(0)
	if got := lit.Type(lang); got != "null_literal" {
		t.Fatalf("child type = %q, want null_literal; tree=%s", got, root.SExpr(lang))
	}
	if !lit.IsNamed() {
		t.Fatalf("null_literal IsNamed = false")
	}
	if got := lit.ChildCount(); got != 0 {
		t.Fatalf("null_literal child count = %d, want 0; tree=%s", got, root.SExpr(lang))
	}
}

func TestPlainVisibleIdentifierStringRuleWithWordRemainsNonterminal(t *testing.T) {
	g := NewGrammar("plain_visible_identifier_string_rule_with_word_nonterminal")
	g.Define("source_file", Sym("call"))
	g.Define("call", Seq(Sym("identifier"), Str("("), Str(")")))
	g.Define("match", Str("match"))
	g.Define("identifier", Pat(`[a-z]+`))
	g.SetWord("identifier")

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	foundNonterminal := false
	for _, sym := range ng.Symbols {
		if sym.Name != "match" {
			continue
		}
		if sym.Kind == SymbolNamedToken {
			t.Fatalf("match was promoted to SymbolNamedToken")
		}
		if sym.Kind == SymbolNonterminal {
			foundNonterminal = true
		}
	}
	if !foundNonterminal {
		t.Fatalf("match nonterminal not found")
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("match()"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
	if got, want := root.SExpr(lang), "(source_file (call (identifier)))"; got != want {
		t.Fatalf("SExpr = %s, want %s", got, want)
	}
}

func TestReferencedPlainVisibleIdentifierStringRuleWithWordBecomesNamedLeaf(t *testing.T) {
	g := NewGrammar("referenced_plain_visible_identifier_string_rule_with_word")
	g.Define("source_file", Choice(Sym("identifier"), Sym("match")))
	g.Define("identifier", Pat(`[a-z]+`))
	g.Define("match", Str("match"))
	g.SetWord("identifier")

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "match"); got != SymbolNamedToken {
		t.Fatalf("match kind = %v, want SymbolNamedToken", got)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("match"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
	if got, want := root.SExpr(lang), "(source_file (match))"; got != want {
		t.Fatalf("SExpr = %s, want %s", got, want)
	}
}

func TestPlainVisibleIdentifierReferenceReachabilityBoundaries(t *testing.T) {
	newBase := func() *Grammar {
		g := NewGrammar("plain_visible_identifier_reference_boundary")
		g.Define("source_file", Sym("identifier"))
		g.Define("identifier", Pat(`[a-z]+`))
		g.Define("match", Str("match"))
		g.SetWord("identifier")
		return g
	}
	tests := []struct {
		name string
		edit func(*Grammar)
		want bool
	}{
		{
			name: "reachable ordinary rule",
			edit: func(g *Grammar) { g.Rules["source_file"] = Choice(Sym("identifier"), Sym("match")) },
			want: true,
		},
		{
			name: "unreachable rule is pruned",
			edit: func(g *Grammar) { g.Define("unused", Sym("match")) },
			want: false,
		},
		{
			name: "global extra is a parser root",
			edit: func(g *Grammar) { g.SetExtras(Sym("match")) },
			want: true,
		},
		{
			name: "external declaration is a parser root",
			edit: func(g *Grammar) { g.SetExternals(Sym("match")) },
			want: true,
		},
		{
			name: "reserved-only membership is not reachability",
			edit: func(g *Grammar) {
				g.ReservedWordSets = []ReservedWordSet{{Name: "global", Rules: []*Rule{Sym("match")}}}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newBase()
			tt.edit(g)
			if got := symbolNameReferencedElsewhere(g, "match"); got != tt.want {
				t.Fatalf("symbolNameReferencedElsewhere(match) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlainVisibleIdentifierOwnershipAgainstLockedCOracle(t *testing.T) {
	cli := lockedTreeSitterCLI(t)
	tests := []struct {
		name        string
		grammarJS   string
		build       func() *Grammar
		source      string
		wantTree    string
		wantCLISym  bool
		wantGoToken bool
	}{
		{
			name: "reachable",
			grammarJS: `module.exports = grammar({
  name: 'keyword_owner_reachable',
  word: $ => $.identifier,
  rules: {
    source_file: $ => choice($.identifier, $.match),
    identifier: $ => /[a-z]+/,
    match: $ => 'match',
  },
});`,
			build: func() *Grammar {
				g := keywordOwnershipBase("keyword_owner_reachable")
				g.Rules["source_file"] = Choice(Sym("identifier"), Sym("match"))
				return g
			},
			source: "match", wantTree: "(source_file (match))", wantCLISym: true, wantGoToken: true,
		},
		{
			name: "unreachable",
			grammarJS: `module.exports = grammar({
  name: 'keyword_owner_unreachable',
  word: $ => $.identifier,
  rules: {
    source_file: $ => $.identifier,
    identifier: $ => /[a-z]+/,
    match: $ => 'match',
    unused: $ => $.match,
  },
});`,
			build: func() *Grammar {
				g := keywordOwnershipBase("keyword_owner_unreachable")
				g.Define("unused", Sym("match"))
				return g
			},
			source: "word", wantTree: "(source_file (identifier))", wantCLISym: false, wantGoToken: false,
		},
		{
			name: "extra",
			grammarJS: `module.exports = grammar({
  name: 'keyword_owner_extra',
  word: $ => $.identifier,
  extras: $ => [/\s/, $.match],
  rules: {
    source_file: $ => $.identifier,
    identifier: $ => /[a-z]+/,
    match: $ => 'match',
  },
});`,
			build: func() *Grammar {
				g := keywordOwnershipBase("keyword_owner_extra")
				g.SetExtras(Pat(`\s`), Sym("match"))
				return g
			},
			source: "match word", wantTree: "(source_file (match) (identifier))", wantCLISym: true, wantGoToken: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.build()
			ng, err := Normalize(g)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if got := symbolKind(t, ng, "match") == SymbolNamedToken; got != tt.wantGoToken {
				t.Fatalf("Go match named-token ownership = %v, want %v", got, tt.wantGoToken)
			}
			lang, err := GenerateLanguage(g)
			if err != nil {
				t.Fatalf("GenerateLanguage: %v", err)
			}
			tree, err := gotreesitter.NewParser(lang).Parse([]byte(tt.source))
			if err != nil {
				t.Fatalf("Go parse: %v", err)
			}
			defer tree.Release()
			if got := tree.RootNode().SExpr(lang); got != tt.wantTree {
				t.Fatalf("Go tree = %s, want %s", got, tt.wantTree)
			}

			parserC := runLockedCLIParserOracle(t, cli, g.Name, tt.grammarJS, tt.source, tt.wantTree)
			if got := cSymbolIsVisibleNamed(parserC, "sym_match"); got != tt.wantCLISym {
				t.Fatalf("locked CLI/C match ownership = %v, want %v", got, tt.wantCLISym)
			}
		})
	}
}

func TestReservedOnlyIdentifierOwnershipAgainstTreeSitterCLI(t *testing.T) {
	cli := lockedTreeSitterCLI(t)
	g := keywordOwnershipBase("keyword_owner_reserved_only")
	g.ReservedWordSets = []ReservedWordSet{{Name: "global", Rules: []*Rule{Sym("match")}}}
	if symbolNameReferencedElsewhere(g, "match") {
		t.Fatal("reserved-only membership unexpectedly made match reachable")
	}
	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "match"); got == SymbolNamedToken {
		t.Fatalf("reserved-only match kind = %v, must not own a keyword token", got)
	}

	tmp := t.TempDir()
	grammarJS := `module.exports = grammar({
  name: 'keyword_owner_reserved_only',
  word: $ => $.identifier,
  reserved: { global: $ => [$.match] },
  rules: {
    source_file: $ => reserved('global', $.identifier),
    identifier: $ => /[a-z]+/,
    match: $ => 'match',
  },
});`
	if err := os.WriteFile(filepath.Join(tmp, "grammar.js"), []byte(grammarJS), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cmd := exec.Command(cli, "generate", "--abi", "14", "--js-runtime", "native")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Undefined symbol `match`") {
		t.Fatalf("tree-sitter CLI reserved-only result = %v\n%s; want fail-closed unreachable match", err, out)
	}
}

func TestExternalIdentifierOwnershipAgainstTreeSitterCLI(t *testing.T) {
	cli := lockedTreeSitterCLI(t)
	g := keywordOwnershipBase("keyword_owner_external")
	g.SetExternals(Sym("match"))
	if !symbolNameReferencedElsewhere(g, "match") {
		t.Fatal("external declaration was not treated as a parser root")
	}

	tmp := t.TempDir()
	grammarJS := `module.exports = grammar({
  name: 'keyword_owner_external',
  word: $ => $.identifier,
  externals: $ => [$.match],
  rules: {
    source_file: $ => choice($.identifier, $.match),
    identifier: $ => /[a-z]+/,
  },
});`
	if err := os.WriteFile(filepath.Join(tmp, "grammar.js"), []byte(grammarJS), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cmd := exec.Command(cli, "generate", "--abi", "14", "--js-runtime", "native")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tree-sitter generate external oracle: %v\n%s", err, out)
	}
	parserC, err := os.ReadFile(filepath.Join(tmp, "src", "parser.c"))
	if err != nil {
		t.Fatalf("read external oracle parser.c: %v", err)
	}
	if !cSymbolIsVisibleNamed(string(parserC), "sym_match") {
		t.Fatal("tree-sitter CLI did not give the external match symbol visible/named ownership")
	}
}

func keywordOwnershipBase(name string) *Grammar {
	g := NewGrammar(name)
	g.Define("source_file", Sym("identifier"))
	g.Define("identifier", Pat(`[a-z]+`))
	g.Define("match", Str("match"))
	g.SetWord("identifier")
	return g
}

func lockedTreeSitterCLI(t *testing.T) string {
	t.Helper()
	cli, err := exec.LookPath("tree-sitter")
	if err != nil {
		t.Skip("tree-sitter CLI is unavailable")
	}
	out, err := exec.Command(cli, "--version").CombinedOutput()
	if err != nil {
		t.Skipf("tree-sitter CLI version probe failed: %v", err)
	}
	const locked = "tree-sitter 0.26.6"
	if strings.TrimSpace(string(out)) != locked {
		t.Skipf("keyword ownership oracle requires %s, got %s", locked, strings.TrimSpace(string(out)))
	}
	return cli
}

func runLockedCLIParserOracle(t *testing.T, cli, name, grammarJS, source, wantTree string) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "grammar.js"), []byte(grammarJS), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cmd := exec.Command(cli, "generate", "--abi", "14", "--js-runtime", "native")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tree-sitter generate: %v\n%s", err, out)
	}
	parserPath := filepath.Join(tmp, "src", "parser.c")
	parserC, err := os.ReadFile(parserPath)
	if err != nil {
		t.Fatalf("read CLI parser.c: %v", err)
	}
	runtimeDir := lockedTreeSitterRuntimeDir(t)
	lockedHeader, err := os.ReadFile(filepath.Join(runtimeDir, "src", "parser.h"))
	if err != nil {
		t.Fatalf("read locked parser.h: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "tree_sitter", "parser.h"), lockedHeader, 0o644); err != nil {
		t.Fatalf("replace parser.h with locked runtime header: %v", err)
	}
	mainSource := fmt.Sprintf(`#include <stdlib.h>
#include <string.h>
#include <tree_sitter/api.h>
const TSLanguage *tree_sitter_%s(void);
int main(void) {
  TSParser *parser = ts_parser_new();
  if (!parser || !ts_parser_set_language(parser, tree_sitter_%s())) return 10;
  TSTree *tree = ts_parser_parse_string(parser, NULL, %q, %d);
  if (!tree) return 11;
  char *sexp = ts_node_string(ts_tree_root_node(tree));
  int ok = sexp && strcmp(sexp, %q) == 0;
  free(sexp);
  ts_tree_delete(tree);
  ts_parser_delete(parser);
  return ok ? 0 : 12;
}
`, name, name, source, len(source), wantTree)
	mainPath := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(mainPath, []byte(mainSource), 0o644); err != nil {
		t.Fatalf("write oracle main.c: %v", err)
	}
	artifact := filepath.Join(tmp, "oracle")
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler is unavailable")
	}
	args := []string{"-std=c11", "-O0", "-D_DEFAULT_SOURCE", "-I" + filepath.Join(tmp, "src"), "-I" + filepath.Join(runtimeDir, "include"), "-I" + filepath.Join(runtimeDir, "src"), parserPath, filepath.Join(runtimeDir, "src", "lib.c"), mainPath, "-pthread", "-o", artifact}
	if out, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		t.Fatalf("compile locked C oracle: %v\n%s", err, out)
	}
	if out, err := exec.Command(artifact).CombinedOutput(); err != nil {
		t.Fatalf("run locked C oracle: %v\n%s", err, out)
	}
	return string(parserC)
}

func cSymbolIsVisibleNamed(parserC, cSymbol string) bool {
	start := strings.Index(parserC, "static const TSSymbolMetadata ts_symbol_metadata[]")
	if start < 0 {
		return false
	}
	body := parserC[start:]
	marker := "[" + cSymbol + "] = {"
	start = strings.Index(body, marker)
	if start < 0 {
		return false
	}
	body = body[start+len(marker):]
	end := strings.Index(body, "},")
	if end < 0 {
		return false
	}
	block := body[:end]
	return strings.Contains(block, ".visible = true") && strings.Contains(block, ".named = true")
}

func TestPlainVisiblePunctuationPrefixedStringRuleWithWordBecomesNamedLeafToken(t *testing.T) {
	g := NewGrammar("plain_visible_punctuation_prefixed_string_rule_with_word_named_leaf_token")
	g.Define("source_file", Seq(Sym("identifier"), Sym("important")))
	g.Define("identifier", Pat(`[a-z]+`))
	g.Define("important", Str("!important"))
	g.Define("match", Str("match"))
	g.SetWord("identifier")
	g.Extras = []*Rule{Pat(`[ \t]+`)}

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "important"); got != SymbolNamedToken {
		t.Fatalf("important kind = %v, want SymbolNamedToken", got)
	}
	foundMatchNonterminal := false
	for _, sym := range ng.Symbols {
		if sym.Name != "match" {
			continue
		}
		if sym.Kind == SymbolNamedToken {
			t.Fatal("match was promoted to SymbolNamedToken")
		}
		if sym.Kind == SymbolNonterminal {
			foundMatchNonterminal = true
		}
	}
	if !foundMatchNonterminal {
		t.Fatal("match nonterminal not found")
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("selector !important"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse missing root node")
	}
	if root.HasError() || root.ChildCount() != 2 {
		t.Fatalf("root = %v, child count = %d; tree=%s", root, root.ChildCount(), root.SExpr(lang))
	}
	important := root.Child(1)
	if got := important.Type(lang); got != "important" {
		t.Fatalf("child type = %q, want important; tree=%s", got, root.SExpr(lang))
	}
	if !important.IsNamed() {
		t.Fatal("important IsNamed = false")
	}
	if got := important.ChildCount(); got != 0 {
		t.Fatalf("important child count = %d, want 0; tree=%s", got, root.SExpr(lang))
	}
}

func TestPlainVisiblePunctuationStringRuleBecomesNamedLeafToken(t *testing.T) {
	g := NewGrammar("plain_visible_punctuation_string_rule_named_leaf_token")
	g.Define("source_file", Sym("optional_chain"))
	g.Define("optional_chain", Str("?."))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "optional_chain"); got != SymbolNamedToken {
		t.Fatalf("optional_chain kind = %v, want SymbolNamedToken", got)
	}
	for _, sym := range ng.Symbols {
		if sym.Name == "?." && sym.Kind == SymbolTerminal {
			t.Fatalf("plain named punctuation string rule also registered anonymous %q terminal", sym.Name)
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("?."))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	lit := root.Child(0)
	if got := lit.Type(lang); got != "optional_chain" {
		t.Fatalf("child type = %q, want optional_chain; tree=%s", got, root.SExpr(lang))
	}
	if !lit.IsNamed() {
		t.Fatalf("optional_chain IsNamed = false")
	}
	if got := lit.ChildCount(); got != 0 {
		t.Fatalf("optional_chain child count = %d, want 0; tree=%s", got, root.SExpr(lang))
	}
}

func TestPlainVisibleEscapedStringRulesBecomeNamedLeafTokens(t *testing.T) {
	g := NewGrammar("plain_visible_escaped_string_rules_named_leaf_tokens")
	g.Define("source_file", Seq(Sym("boundary_assertion"), Sym("non_boundary_assertion")))
	g.Define("boundary_assertion", Str("\\b"))
	g.Define("non_boundary_assertion", Str("\\B"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, name := range []string{"boundary_assertion", "non_boundary_assertion"} {
		if got := symbolKind(t, ng, name); got != SymbolNamedToken {
			t.Fatalf("%s kind = %v, want SymbolNamedToken", name, got)
		}
		sym := symbolID(t, ng, name)
		if sym >= ng.TokenCount() {
			t.Fatalf("%s symbol = %d outside token range %d", name, sym, ng.TokenCount())
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("\\b\\B"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 2 {
		t.Fatalf("root = %v, child count = %d; tree=%s", root, root.ChildCount(), root.SExpr(lang))
	}
	for i, name := range []string{"boundary_assertion", "non_boundary_assertion"} {
		node := root.Child(i)
		if got := node.Type(lang); got != name {
			t.Fatalf("child %d type = %q, want %s; tree=%s", i, got, name, root.SExpr(lang))
		}
		if !node.IsNamed() {
			t.Fatalf("%s IsNamed = false", name)
		}
		if got := node.ChildCount(); got != 0 {
			t.Fatalf("%s child count = %d, want 0; tree=%s", name, got, root.SExpr(lang))
		}
	}
}

func TestPlainVisiblePatternRuleBecomesNamedLeafToken(t *testing.T) {
	g := NewGrammar("plain_visible_pattern_rule_named_leaf_token")
	g.Define("source_file", Sym("any_character"))
	g.Define("any_character", Pat(`.`))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "any_character"); got != SymbolNamedToken {
		t.Fatalf("any_character kind = %v, want SymbolNamedToken", got)
	}
	anyCharacterSym := symbolID(t, ng, "any_character")
	if anyCharacterSym >= ng.TokenCount() {
		t.Fatalf("any_character symbol = %d outside token range %d", anyCharacterSym, ng.TokenCount())
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("."))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	node := root.Child(0)
	if got := node.Type(lang); got != "any_character" {
		t.Fatalf("child type = %q, want any_character; tree=%s", got, root.SExpr(lang))
	}
	if !node.IsNamed() {
		t.Fatal("any_character IsNamed = false")
	}
	if got := node.ChildCount(); got != 0 {
		t.Fatalf("any_character child count = %d, want 0; tree=%s", got, root.SExpr(lang))
	}
}

func TestPlainVisiblePunctuationStringRuleWithAnonymousSourceStaysWrapper(t *testing.T) {
	g := NewGrammar("plain_visible_punctuation_string_rule_anonymous_collision")
	g.Define("source_file", Seq(Sym("optional_chain"), Str("?.")))
	g.Define("optional_chain", Str("?."))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "optional_chain"); got != SymbolNonterminal {
		t.Fatalf("optional_chain kind = %v, want SymbolNonterminal", got)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("?.?."))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse missing root node")
	}
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
	wrapper := root.Child(0)
	if got := wrapper.Type(lang); got != "optional_chain" {
		t.Fatalf("first child type = %q, want optional_chain; tree=%s", got, root.SExpr(lang))
	}
	if got := wrapper.ChildCount(); got != 1 {
		t.Fatalf("optional_chain child count = %d, want wrapper child; tree=%s", got, root.SExpr(lang))
	}
	child := wrapper.Child(0)
	if got := child.Type(lang); got != "?." || child.IsNamed() {
		t.Fatalf("wrapped child = (%q named=%v), want anonymous ?.; tree=%s", got, child.IsNamed(), root.SExpr(lang))
	}
}

func TestBareStringChoiceStaysNonterminal(t *testing.T) {
	g := NewGrammar("bare_string_choice_nonterminal")
	g.Define("source_file", Seq(Sym("identifier"), Sym("relational_operator"), Sym("identifier")))
	g.Define("identifier", Pat(`[a-z]+`))
	g.Define("relational_operator", Choice(
		Str("<"),
		Str(">"),
		Str("<="),
		Str(">="),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, sym := range ng.Symbols {
		if sym.Name != "relational_operator" {
			continue
		}
		if sym.Kind != SymbolNonterminal {
			t.Fatalf("relational_operator kind = %v, want SymbolNonterminal", sym.Kind)
		}
		return
	}
	t.Fatal("relational_operator symbol not found")
}

func TestHiddenBareStringSharingBareStringChoiceBecomesNonterminal(t *testing.T) {
	g := NewGrammar("hidden_bare_string_choice_collision")
	g.Define("source_file", Seq(Sym("_bang"), Sym("operator")))
	g.Define("_bang", Str("!"))
	g.Define("operator", Choice(
		Str("!"),
		Str("?"),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, name := range []string{"_bang", "operator"} {
		if got := symbolKind(t, ng, name); got != SymbolNonterminal {
			t.Fatalf("%s kind = %v, want SymbolNonterminal", name, got)
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("!!"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse missing root node")
	}
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
}

func TestHiddenStringTokenSharingAnonymousLiteralBecomesNonterminal(t *testing.T) {
	g := NewGrammar("hidden_string_token_literal_collision")
	g.Define("source_file", Seq(Sym("_semi"), Str(";")))
	g.Define("_semi", Token(Str(";")))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "_semi"); got != SymbolNonterminal {
		t.Fatalf("_semi kind = %v, want SymbolNonterminal", got)
	}

	semicolonTerminals := 0
	for _, term := range ng.Terminals {
		if ng.Symbols[term.SymbolID].Name == ";" {
			semicolonTerminals++
		}
	}
	if semicolonTerminals != 1 {
		t.Fatalf("semicolon terminal count = %d, want 1", semicolonTerminals)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte(";;"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse missing root node")
	}
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
}

func TestHiddenStringTokenDuplicatesWithoutAnonymousLiteralRemainTokens(t *testing.T) {
	g := NewGrammar("hidden_string_token_token_only_collision")
	g.Define("source_file", Seq(Sym("_bang"), Sym("_also_bang")))
	g.Define("_bang", Token(Str("!")))
	g.Define("_also_bang", Token(Str("!")))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, name := range []string{"_bang", "_also_bang"} {
		if got := symbolKind(t, ng, name); got != SymbolNamedToken {
			t.Fatalf("%s kind = %v, want SymbolNamedToken", name, got)
		}
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("!!"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse missing root node")
	}
	if root.HasError() {
		t.Fatalf("parse has error: %s", root.SExpr(lang))
	}
	if got := root.SExpr(lang); got != "(source_file)" {
		t.Fatalf("SExpr = %s, want (source_file)", got)
	}
}

func TestPrecedenceWrappedBareStringChoiceStaysNonterminal(t *testing.T) {
	g := NewGrammar("prec_wrapped_bare_string_choice_nonterminal")
	g.Define("source_file", Sym("operator"))
	g.Define("operator", Prec(1, Choice(
		Str("!"),
		Str("?"),
	)))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "operator"); got != SymbolNonterminal {
		t.Fatalf("operator kind = %v, want SymbolNonterminal", got)
	}
}

func TestPrecRightStringChoiceSymbolStaysVisibleWrapper(t *testing.T) {
	g := NewGrammar("prec_right_string_choice_symbol_wrapper")
	g.Define("source_file", Sym("init_class"))
	g.Define("init_class", PrecRight(0, Choice(
		Str("="),
		Str("+="),
		Str("-="),
	)))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	initClassID := -1
	equalsID := -1
	for id, sym := range ng.Symbols {
		switch {
		case sym.Name == "init_class":
			initClassID = id
			if sym.Kind != SymbolNonterminal || !sym.Visible || !sym.Named {
				t.Fatalf("init_class symbol = %+v, want visible named nonterminal", sym)
			}
		case sym.Name == "=" && sym.Kind == SymbolTerminal && !sym.Named:
			equalsID = id
		}
	}
	if initClassID < 0 {
		t.Fatal("missing init_class symbol")
	}
	if equalsID < 0 {
		t.Fatal("missing anonymous = terminal")
	}
	for _, term := range ng.Terminals {
		if term.SymbolID == initClassID {
			t.Fatalf("init_class was emitted as a terminal pattern")
		}
	}

	hasEqualsProduction := false
	for _, prod := range ng.Productions {
		if prod.LHS == initClassID && len(prod.RHS) == 1 && prod.RHS[0] == equalsID {
			hasEqualsProduction = true
			break
		}
	}
	if !hasEqualsProduction {
		t.Fatal("missing init_class -> \"=\" wrapper production")
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("="))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	wrapper := root.Child(0)
	if got := wrapper.Type(lang); got != "init_class" {
		t.Fatalf("child type = %q, want init_class; tree=%s", got, root.SExpr(lang))
	}
	if got := wrapper.ChildCount(); got != 1 {
		t.Fatalf("init_class child count = %d, want anonymous operator child; tree=%s", got, root.SExpr(lang))
	}
	child := wrapper.Child(0)
	if got := child.Type(lang); got != "=" || child.IsNamed() {
		t.Fatalf("wrapped child = (%q named=%v), want anonymous =; tree=%s", got, child.IsNamed(), root.SExpr(lang))
	}
}

func TestPrecedenceWrappedPlainStringRuleStaysWrapper(t *testing.T) {
	g := NewGrammar("prec_wrapped_plain_string_rule_wrapper")
	g.Define("source_file", Sym("null_literal"))
	g.Define("null_literal", Prec(1, Str("null")))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "null_literal"); got != SymbolNonterminal {
		t.Fatalf("null_literal kind = %v, want SymbolNonterminal", got)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("null"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	lit := root.Child(0)
	if got := lit.Type(lang); got != "null_literal" {
		t.Fatalf("child type = %q, want null_literal; tree=%s", got, root.SExpr(lang))
	}
	if got := lit.ChildCount(); got != 1 {
		t.Fatalf("null_literal child count = %d, want wrapper child; tree=%s", got, root.SExpr(lang))
	}
	child := lit.Child(0)
	if got := child.Type(lang); got != "null" || child.IsNamed() {
		t.Fatalf("wrapped child = (%q named=%v), want anonymous null; tree=%s", got, child.IsNamed(), root.SExpr(lang))
	}
}

func TestDynamicPrecedenceStringRuleStaysVisibleWrapper(t *testing.T) {
	g := NewGrammar("dynamic_prec_string_rule_wrapper")
	g.Define("source_file", Sym("implicit_type"))
	g.Define("implicit_type", PrecDynamic(1, Str("var")))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "implicit_type"); got != SymbolNonterminal {
		t.Fatalf("implicit_type kind = %v, want SymbolNonterminal", got)
	}
	implicitTypeSym := symbolID(t, ng, "implicit_type")
	if implicitTypeSym < ng.TokenCount() {
		t.Fatalf("implicit_type symbol = %d inside token range %d", implicitTypeSym, ng.TokenCount())
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("var"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	wrapper := root.Child(0)
	if got := wrapper.Type(lang); got != "implicit_type" {
		t.Fatalf("child type = %q, want implicit_type; tree=%s", got, root.SExpr(lang))
	}
	if got := wrapper.ChildCount(); got != 1 {
		t.Fatalf("implicit_type child count = %d, want wrapper child; tree=%s", got, root.SExpr(lang))
	}
	child := wrapper.Child(0)
	if got := child.Type(lang); got != "var" || child.IsNamed() {
		t.Fatalf("wrapped child = (%q named=%v), want anonymous var; tree=%s", got, child.IsNamed(), root.SExpr(lang))
	}
}

func TestChoiceWrapperWithNonterminalAlternativeStaysVisibleWrapper(t *testing.T) {
	g := NewGrammar("choice_wrapper_with_nonterminal_alternative")
	g.Define("source_file", Sym("last_field"))
	g.Define("last_field", Choice(
		Sym("field_decl"),
		Str(".."),
	))
	g.Define("field_decl", Seq(Sym("identifier"), Str("="), Sym("identifier")))
	g.Define("identifier", Pat(`[a-z]+`))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "last_field"); got != SymbolNonterminal {
		t.Fatalf("last_field kind = %v, want SymbolNonterminal", got)
	}
	lastFieldSym := symbolID(t, ng, "last_field")
	if lastFieldSym < ng.TokenCount() {
		t.Fatalf("last_field symbol = %d inside token range %d", lastFieldSym, ng.TokenCount())
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte(".."))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || root.ChildCount() != 1 {
		t.Fatalf("root = %v, child count = %d", root, root.ChildCount())
	}
	wrapper := root.Child(0)
	if got := wrapper.Type(lang); got != "last_field" {
		t.Fatalf("child type = %q, want last_field; tree=%s", got, root.SExpr(lang))
	}
	if got := wrapper.ChildCount(); got != 1 {
		t.Fatalf("last_field child count = %d, want wrapper child; tree=%s", got, root.SExpr(lang))
	}
	child := wrapper.Child(0)
	if got := child.Type(lang); got != ".." || child.IsNamed() {
		t.Fatalf("wrapped child = (%q named=%v), want anonymous ..; tree=%s", got, child.IsNamed(), root.SExpr(lang))
	}
}

func TestCapitalizedPlainStringRuleStaysWrapper(t *testing.T) {
	g := NewGrammar("capitalized_plain_string_rule_wrapper")
	g.Define("source_file", Sym("true"))
	g.Define("true", Str("True"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "true"); got != SymbolNonterminal {
		t.Fatalf("true kind = %v, want SymbolNonterminal", got)
	}
}

func TestTokenWrappedBareStringChoiceRemainsNamedToken(t *testing.T) {
	g := NewGrammar("token_wrapped_bare_string_choice_named_token")
	g.Define("source_file", Sym("operator_token"))
	g.Define("operator_token", Token(Choice(
		Str("!"),
		Str("?"),
	)))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := symbolKind(t, ng, "operator_token"); got != SymbolNamedToken {
		t.Fatalf("operator_token kind = %v, want SymbolNamedToken", got)
	}
}

func TestAliasedInlinePatternWinsSameLengthNamedPatternTie(t *testing.T) {
	g := NewGrammar("aliased_inline_pattern_precedence")
	g.Define("source_file", Choice(
		Sym("preproc_include"),
		Sym("preproc_call"),
	))
	g.Define("preproc_include", Seq(
		Alias(Pat(`#[ \t]*include`), "#include", false),
		Sym("system_lib_string"),
		ImmToken(Pat(`\r?\n`)),
	))
	g.Define("preproc_call", Seq(
		Field("directive", Sym("preproc_directive")),
		Optional(Field("argument", Sym("preproc_arg"))),
		ImmToken(Pat(`\r?\n`)),
	))
	g.Define("preproc_directive", Pat(`#[ \t]*[a-zA-Z0-9]\w*`))
	g.Define("preproc_arg", Token(Pat(`[^\r\n]+`)))
	g.Define("system_lib_string", Token(Seq(
		Str("<"),
		Repeat(Pat(`[^>\n]`)),
		Str(">"),
	)))
	g.Extras = []*Rule{Pat(`[ \t]+`)}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("#include <iostream>\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	if got := tree.RootNode().SExpr(lang); got != "(source_file (preproc_include (system_lib_string)))" {
		t.Fatalf("SExpr = %s, want (source_file (preproc_include (system_lib_string)))", got)
	}
}

func TestUppercaseUnicodeEscapeIdentifierDoesNotCaptureDigits(t *testing.T) {
	g := NewGrammar("unicode_escape_identifier_digit_split")
	g.Define("source_file", Choice(
		Sym("upper_case_identifier"),
		Sym("number_literal"),
	))
	g.Define("upper_case_identifier", Pat(`[A-Z\U00010400-\U00010427][A-Z0-9]*`))
	g.Define("number_literal", Pat(`[0-9]+`))

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	if got := tree.RootNode().SExpr(lang); got != "(source_file (number_literal))" {
		t.Fatalf("SExpr = %s, want (source_file (number_literal))", got)
	}
}

func symbolKind(t *testing.T, ng *NormalizedGrammar, name string) SymbolKind {
	t.Helper()
	for _, sym := range ng.Symbols {
		if sym.Name == name {
			return sym.Kind
		}
	}
	t.Fatalf("%s symbol not found", name)
	return SymbolTerminal
}

func symbolID(t *testing.T, ng *NormalizedGrammar, name string) int {
	t.Helper()
	for i, sym := range ng.Symbols {
		if sym.Name == name {
			return i
		}
	}
	t.Fatalf("%s symbol not found", name)
	return -1
}
