package grammargen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportGrammarJSSkipsCommentsInSemanticChildren(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "grammar_js", "bitbake_comments.js"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	g, err := ImportGrammarJS(source)
	if err != nil {
		t.Fatalf("ImportGrammarJS: %v", err)
	}
	if g.Name != "bitbake" {
		t.Fatalf("name = %q, want bitbake", g.Name)
	}
	if got, want := len(g.Externals), 13; got != want {
		t.Fatalf("externals = %d, want %d", got, want)
	}
	if got, want := len(g.Extras), 3; got != want {
		t.Fatalf("extras = %d, want %d", got, want)
	}
	if _, ok := g.Rules["recipe"]; !ok {
		t.Fatal("recipe rule is missing")
	}
}

func TestImportGrammarJSSkipsCommentInParenthesizedRule(t *testing.T) {
	source := []byte(`
module.exports = grammar({
  name: 'parenthesized_comment',
  rules: {
    source_file: $ => (
      // This comment is not a rule expression.
      field('value', $.value)
    ),
    value: $ => 'value',
  },
})
`)

	g, err := ImportGrammarJS(source)
	if err != nil {
		t.Fatalf("ImportGrammarJS: %v", err)
	}
	rule := g.Rules["source_file"]
	if rule == nil || rule.Kind != RuleField || rule.Value != "value" {
		t.Fatalf("source_file = %#v, want value field", rule)
	}
}
