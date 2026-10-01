package search

import (
	"errors"
	"strings"
	"testing"
)

func TestParseQueryImplicitAnd(t *testing.T) {
	node, err := ParseQuery("alpha beta")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok := node.(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("node = %#v, want And with 2 text terms", node)
	}
}

func TestParseQueryBooleanPrecedence(t *testing.T) {
	node, err := ParseQuery("alpha OR beta AND gamma")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	or, ok := node.(OrExpr)
	if !ok || len(or.Exprs) != 2 {
		t.Fatalf("top = %#v, want Or", or)
	}
	and, ok := or.Exprs[1].(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("rhs = %#v, want And", or.Exprs[1])
	}
}

func TestParseQueryUnknownFieldIsProse(t *testing.T) {
	node, err := ParseQuery("bogus:value")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	text, ok := node.(TextExpr)
	if !ok || text.Text != "bogus:value" {
		t.Fatalf("node = %#v, want TextExpr bogus:value", node)
	}
}

func TestParseQueryProseFromTranscript(t *testing.T) {
	cases := []struct {
		q    string
		want []string
	}{
		{"don't break", []string{"don't", "break"}},
		{"Error: failed", []string{"Error:", "failed"}},
		{"http://example.com", []string{"http://example.com"}},
		{"it's working", []string{"it's", "working"}},
		{"hello, world", []string{"hello", "world"}},
		{"C++", []string{"C"}},
		{"please fix the bug", []string{"please", "fix", "the", "bug"}},
	}
	for _, tc := range cases {
		node, err := ParseQuery(tc.q)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", tc.q, err)
		}
		got := textTerms(node)
		if len(got) != len(tc.want) {
			t.Fatalf("ParseQuery(%q) terms = %v, want %v", tc.q, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("ParseQuery(%q) terms = %v, want %v", tc.q, got, tc.want)
			}
		}
	}
}

func textTerms(node Node) []string {
	switch v := node.(type) {
	case TextExpr:
		return []string{v.Text}
	case AndExpr:
		var out []string
		for _, e := range v.Exprs {
			out = append(out, textTerms(e)...)
		}
		return out
	default:
		return nil
	}
}

func TestParseQueryProjectScopeToken(t *testing.T) {
	node, err := ParseQuery("project:current kind:web")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok := node.(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("node = %#v", node)
	}
}

func TestQuotedTermIsPhrase(t *testing.T) {
	node, err := ParseQuery(`"quick brown" plain`)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok := node.(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("node = %#v", node)
	}
	phrase, ok := and.Exprs[0].(TextExpr)
	if !ok || !phrase.Phrase || phrase.Text != "quick brown" {
		t.Fatalf("phrase = %#v", and.Exprs[0])
	}
	bare, ok := and.Exprs[1].(TextExpr)
	if !ok || bare.Phrase {
		t.Fatalf("bare = %#v", and.Exprs[1])
	}
}

func TestQuotedEscapesResolve(t *testing.T) {
	node, err := ParseQuery(`"say \"hi\" twice"`)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	text, ok := node.(TextExpr)
	if !ok || text.Text != `say "hi" twice` {
		t.Fatalf("text = %#v", node)
	}
	// Unescaped backslashes stay literal so Windows paths survive.
	node, err = ParseQuery(`"C:\temp\file"`)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if text, ok = node.(TextExpr); !ok || text.Text != `C:\temp\file` {
		t.Fatalf("text = %#v", node)
	}
}

func TestParseQueryPastedProseNeverFails(t *testing.T) {
	cases := []struct {
		q    string
		want []string
	}{
		// Em-dash and other punctuation outside the word set separate words.
		{"plus one orient call — named wording/settings files", []string{"plus", "one", "orient", "call", "named", "wording/settings", "files"}},
		{"a -> b => c", []string{"a", "-", "b", "c"}},
		{"emoji 🐛 here", []string{"emoji", "here"}},
		// Sentence-final dots come off; dots inside a word stay.
		{"need no tree survey. fmt.Println ...", []string{"need", "no", "tree", "survey", "fmt.Println"}},
		// Lowercase operator words are ordinary words.
		{"deliverables are not nameable", []string{"deliverables", "are", "not", "nameable"}},
		{"and then or else", []string{"and", "then", "or", "else"}},
		{"Not Only", []string{"Not", "Only"}},
		// Stray closing parenthesis and an unclosed group both parse.
		{"foo) bar", []string{"foo", "bar"}},
		{"(foo bar", []string{"foo", "bar"}},
		{"fmt.Println(x)", []string{"fmt.Println", "x"}},
		// Operators with nothing to join are dropped.
		{"foo AND", []string{"foo"}},
		{"OR foo", []string{"foo"}},
		{"foo NOT", []string{"foo"}},
	}
	for _, tc := range cases {
		node, err := ParseQuery(tc.q)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", tc.q, err)
		}
		got := textTerms(node)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("ParseQuery(%q) terms = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestParseQueryAdjacentOperatorsKeepTheBinaryOne(t *testing.T) {
	node, err := ParseQuery("foo AND OR bar")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	or, ok := node.(OrExpr)
	if !ok || len(or.Exprs) != 2 {
		t.Fatalf("node = %#v, want Or with 2 terms", node)
	}
}

func TestParseQueryOperatorsAreUppercaseOnly(t *testing.T) {
	node, err := ParseQuery("alpha NOT kind:web")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok := node.(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("node = %#v", node)
	}
	if _, ok := and.Exprs[1].(NotExpr); !ok {
		t.Fatalf("rhs = %#v, want NotExpr", and.Exprs[1])
	}
	node, err = ParseQuery("alpha not kind:web")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok = node.(AndExpr)
	if !ok || len(and.Exprs) != 3 {
		t.Fatalf("node = %#v, want three conjuncts with 'not' as a word", node)
	}
}

func TestUnterminatedQuoteRunsToEnd(t *testing.T) {
	node, err := ParseQuery(`Nomos "quick bro`)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	and, ok := node.(AndExpr)
	if !ok || len(and.Exprs) != 2 {
		t.Fatalf("node = %#v", node)
	}
	phrase, ok := and.Exprs[1].(TextExpr)
	if !ok || !phrase.Phrase || phrase.Text != "quick bro" {
		t.Fatalf("phrase = %#v", and.Exprs[1])
	}
}

func TestEmptyQuotedTermIsDropped(t *testing.T) {
	node, err := ParseQuery(`"" alpha`)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if text, ok := node.(TextExpr); !ok || text.Text != "alpha" {
		t.Fatalf("node = %#v, want TextExpr alpha", node)
	}
	_, err = ParseQuery(`"`)
	pe := &ParseError{}
	if err == nil || !errors.As(err, &pe) || !strings.Contains(pe.Message, "no searchable terms") {
		t.Fatalf("err = %v", err)
	}
}

func TestFilterErrorsStillPointAtTheField(t *testing.T) {
	_, err := ParseQuery("alpha kind:")
	pe := &ParseError{}
	if err == nil || !errors.As(err, &pe) || pe.Kind != ParseErrEmptyValue || pe.Field != "kind" {
		t.Fatalf("err = %v", err)
	}
}
