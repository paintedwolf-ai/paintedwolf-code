package litprefilter_test

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
)

func clauseTexts(req litprefilter.Requirement) [][]string {
	out := make([][]string, 0, len(req.Clauses))
	for _, clause := range req.Clauses {
		texts := make([]string, 0, len(clause))
		for _, lit := range clause {
			texts = append(texts, string(lit.Bytes))
		}
		out = append(out, texts)
	}
	return out
}

// Operator-free patterns parse to one OpLiteral.
func TestExtractSubstring(t *testing.T) {
	t.Parallel()
	req := litprefilter.Extract("needle", false)
	if got := clauseTexts(req); !slices.EqualFunc(got, [][]string{{"needle"}}, slices.Equal) || req.Clauses[0][0].Fold {
		t.Fatalf("got %+v", req)
	}
	req = litprefilter.Extract("NeEdLe", true)
	if !req.Matches([]byte("needle")) || !req.Clauses[0][0].Fold {
		t.Fatalf("fold got %+v", req)
	}
}

// A concatenation requires every part; the most selective clauses come first.
func TestExtractKeepsEveryRequiredPartOfAConcatenation(t *testing.T) {
	t.Parallel()
	req := litprefilter.Extract(`foo.*barbaz`, false)
	if got := clauseTexts(req); !slices.EqualFunc(got, [][]string{{"barbaz"}, {"foo"}}, slices.Equal) {
		t.Fatalf("clauses = %v", got)
	}
	if req.Matches([]byte("barbaz only")) || !req.Matches([]byte("foo then barbaz")) {
		t.Fatal("a concatenation needs every part")
	}
}

// An alternation needs one literal from some branch: content holding neither
// cannot match, content holding either may.
func TestExtractJoinsAlternationBranches(t *testing.T) {
	t.Parallel()
	req := litprefilter.Extract(`projection_marker|ProjectionMarker`, false)
	if got := clauseTexts(req); !slices.EqualFunc(got, [][]string{{"projection_marker", "ProjectionMarker"}}, slices.Equal) {
		t.Fatalf("clauses = %v", got)
	}
	for content, want := range map[string]bool{
		"x := projection_marker": true, "type ProjectionMarker struct": true, "projection only": false,
	} {
		if got := req.Matches([]byte(content)); got != want {
			t.Errorf("Matches(%q) = %v want %v", content, got, want)
		}
	}
	nested := litprefilter.Extract(`pre(alpha|beta)post`, false)
	if !nested.Matches([]byte("prebetapost")) || nested.Matches([]byte("pre gamma post")) {
		t.Fatalf("nested alternation clauses = %v", clauseTexts(nested))
	}
}

// A pattern with no literal every match shares requires nothing: prefiltering
// on one branch would drop files that match another.
func TestExtractDeclinesWhenNothingIsRequired(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`foo|.*`, `foo|`, `.*`, `a?`, `(x)*`, ``, `[unclosed`} {
		if req := litprefilter.Extract(pattern, false); !req.Empty() {
			t.Errorf("Extract(%q) claimed %v", pattern, clauseTexts(req))
		}
	}
	// Distinct first letters keep the parser from factoring a shared prefix.
	wide := make([]string, 0, 20)
	for i := range 20 {
		wide = append(wide, string(rune('a'+i))+"branch"+strconv.Itoa(i))
	}
	if req := litprefilter.Extract(strings.Join(wide, "|"), false); !req.Empty() {
		t.Errorf("a %d-way alternation claimed %d literals per file", len(wide), len(req.Clauses[0]))
	}
}

func TestBufferHas(t *testing.T) {
	t.Parallel()
	hay := []byte("aaa NEEDLE bbb")
	if !litprefilter.BufferHas(hay, []byte("NEEDLE"), false) {
		t.Fatal("exact")
	}
	if litprefilter.BufferHas(hay, []byte("missing"), false) {
		t.Fatal("should miss")
	}
	if !litprefilter.BufferHas(hay, bytes.ToLower([]byte("needle")), true) {
		t.Fatal("fold")
	}
	if !litprefilter.BufferHas([]byte("kelvin K"), []byte("k"), true) {
		t.Fatal("Unicode simple fold with different UTF-8 lengths")
	}
	if !litprefilter.BufferHas([]byte("final ς"), []byte("σ"), true) {
		t.Fatal("Unicode simple fold cycle")
	}
	// An empty literal is "nothing required", which every buffer satisfies —
	// otherwise a caller that found no literal would skip every file.
	if !litprefilter.BufferHas([]byte("anything"), nil, false) {
		t.Fatal("empty literal must not skip")
	}
}

// Both callers prefilter with the same answers; disagreeing would change which
// files a search never opens.
func TestExtractAndBufferHasAgree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		fold    bool
		content string
		want    bool
	}{
		{`func \w+\(`, false, "func main() {", true},
		{`func \w+\(`, false, "type T struct{}", false},
		{`ERROR`, true, "unexpected error here", true},
		{`ERROR`, true, "all fine", false},
		{`(?i)ERROR`, false, "unexpected error here", true},
		{`(?-i:ERROR)`, true, "unexpected error here", false},
		{`   `, false, "   ", true},
		{`(?i:LONG)exact`, false, "longexact", true},
		{`İstanbul`, true, "İstanbul", true},
		{`İstanbul`, true, "istanbul", false},
	}
	for _, tc := range cases {
		req := litprefilter.Extract(tc.pattern, tc.fold)
		if req.Empty() {
			t.Fatalf("Extract(%q) found no literal", tc.pattern)
		}
		if got := req.Matches([]byte(tc.content)); got != tc.want {
			t.Errorf("pattern %q against %q = %v want %v (clauses=%v)", tc.pattern, tc.content, got, tc.want, clauseTexts(req))
		}
	}
}
