package textrank

import "testing"

func termsToSet(terms []string) map[string]bool {
	out := make(map[string]bool, len(terms))
	for _, t := range terms {
		out[t] = true
	}
	return out
}

func TestAnalyzeSplitsIdentifiers(t *testing.T) {
	got := termsToSet(Analyze("getUserID", true, true))
	for _, want := range []string{"get", "user", "id", "getuserid"} {
		if !got[want] {
			t.Fatalf("Analyze(getUserID)=%v missing %q", got, want)
		}
	}
	// single-word segment must not double-count as its own compound.
	single := Analyze("session", true, true)
	if len(single) != 1 || single[0] != "session" {
		t.Fatalf("single word should emit once, got %v", single)
	}
}

func TestAnalyzeNaturalLanguageNoSplit(t *testing.T) {
	// With splitIdent off, hyphen/space split but camelCase does not.
	got := termsToSet(Analyze("getUserName dark-mode", false, false))
	if !got["getusername"] {
		t.Fatalf("NL analyze should keep compound lowercased, got %v", got)
	}
	if got["user"] {
		t.Fatalf("NL analyze must not camel-split, got %v", got)
	}
	for _, want := range []string{"dark", "mode"} {
		if !got[want] {
			t.Fatalf("NL analyze should split separators, missing %q in %v", want, got)
		}
	}
}

func TestAnalyzeSingleChars(t *testing.T) {
	got := termsToSet(Analyze("internal/LLM/a_b.go", true, true))
	for _, want := range []string{"internal", "llm", "go"} {
		if !got[want] {
			t.Fatalf("term %q missing from %v", want, got)
		}
	}
	if got["a"] || got["b"] {
		t.Fatalf("single-char tokens should drop, got %v", got)
	}
}

func TestStemFoldsPlurals(t *testing.T) {
	cases := map[string]string{
		"handlers":  "handler",
		"stores":    "store",
		"names":     "name",
		"classes":   "class",
		"boxes":     "box",
		"matches":   "match",
		"libraries": "library",
		"queries":   "query",
		"class":     "class",
		"status":    "status",
		"analysis":  "analysis",
		"process":   "process",
		"go":        "go",
	}
	for in, want := range cases {
		if got := Stem(in); got != want {
			t.Fatalf("Stem(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestStemAlignsQueryAndDoc(t *testing.T) {
	q := termsToSet(AnalyzeQuery("session stores", true, true))
	d := termsToSet(Analyze("SessionStore", true, true))
	if !q["store"] || !d["store"] {
		t.Fatalf("plural query %v should align with doc %v on 'store'", q, d)
	}
}

func TestWithinOneEdit(t *testing.T) {
	yes := [][2]string{{"verify", "verifi"}, {"handler", "handlr"}, {"store", "stort"}, {"token", "tokens"}}
	for _, c := range yes {
		if !WithinOneEdit(c[0], c[1]) {
			t.Fatalf("WithinOneEdit(%q,%q) = false, want true", c[0], c[1])
		}
	}
	no := [][2]string{{"store", "query"}, {"handler", "handling"}, {"cat", "dog"}}
	for _, c := range no {
		if WithinOneEdit(c[0], c[1]) {
			t.Fatalf("WithinOneEdit(%q,%q) = true, want false", c[0], c[1])
		}
	}
}

func TestFieldScoresRareTermBeatsCommon(t *testing.T) {
	docs := [][]Field{
		{{"internal/a.go", 3}, {"internal common code", 1}},
		{{"internal/b.go", 3}, {"internal common code", 1}},
		{{"internal/ignition.go", 3}, {"internal ignition sequence", 1}},
	}
	scores := FieldScores("internal ignition", docs, CodeOptions())
	if scores[2] <= scores[0] || scores[2] <= scores[1] {
		t.Fatalf("rare-term doc should win: %v", scores)
	}
}

func TestFieldScoresPathBeatsBody(t *testing.T) {
	docs := [][]Field{
		{{"ignition/main.go", 3}, {"boot code", 1}},
		{{"boot/main.go", 3}, {"ignition code", 1}},
	}
	scores := FieldScores("ignition", docs, CodeOptions())
	if scores[0] <= scores[1] {
		t.Fatalf("path hit should outweigh body hit: %v", scores)
	}
}

func TestFieldScoresTFSaturation(t *testing.T) {
	docs := [][]Field{
		{{"a.go", 3}, {"auth", 1}},
		{{"b.go", 3}, {"auth auth auth auth auth auth auth auth auth auth", 1}},
	}
	scores := FieldScores("auth", docs, CodeOptions())
	if scores[1] <= scores[0] {
		t.Fatalf("more occurrences should score higher: %v", scores)
	}
	if scores[1] >= 2*scores[0] {
		t.Fatalf("10× frequency must saturate well under 2×: %v", scores)
	}
}

func TestFieldScoresFuzzyRetrievesTypo(t *testing.T) {
	docs := [][]Field{
		{{"verify.go", 3}, {"verify token", 1}},
		{{"other.go", 3}, {"unrelated body", 1}},
	}
	fuzzy := FieldScores("verifyy", docs, CodeOptions())
	exact := FieldScores("verify", docs, CodeOptions())
	if fuzzy[0] <= 0 {
		t.Fatalf("fuzzy query should retrieve doc 0: %v", fuzzy)
	}
	if fuzzy[0] >= exact[0] {
		t.Fatalf("fuzzy score %v should be discounted below exact %v", fuzzy[0], exact[0])
	}
}

func TestCorpusNormalizeInUnitRange(t *testing.T) {
	opt := CodeOptions()
	opt.Normalize = true
	docs := [][]Field{
		{{"auth/login.go", 3}, {"auth login handler", 1}},
		{{"unrelated.go", 3}, {"nothing here", 1}},
	}
	c := Fit(docs, opt)
	full := c.ScoreAnalyzed([]string{"auth", "login"}, docs[0])
	none := c.ScoreAnalyzed([]string{"auth", "login"}, docs[1])
	if full <= 0 || full >= 1 {
		t.Fatalf("normalized full-match score should be in (0,1): %v", full)
	}
	if none != 0 {
		t.Fatalf("no-match doc should be 0: %v", none)
	}
	// A doc matching one of two query terms should score below a full match.
	half := c.ScoreAnalyzed([]string{"auth", "login"}, []Field{{"auth/x.go", 3}, {"only auth", 1}})
	if !(half > 0 && half < full) {
		t.Fatalf("partial match %v should be between 0 and full %v", half, full)
	}
}

func TestCorpusProbeScoredAgainstFittedPool(t *testing.T) {
	// IDF comes from the candidate pool; a fresh probe doc (not in the pool) is
	// scored against that IDF. "ignition" is rare in the pool, "linux" common,
	// so a probe matching the rare term must outscore one matching the common.
	opt := CodeOptions()
	opt.Normalize = true
	pool := [][]Field{
		{{"linux one", 1}}, {{"linux two", 1}}, {{"linux three", 1}}, {{"ignition boot", 1}},
	}
	c := Fit(pool, opt)
	rare := c.ScoreAnalyzed([]string{"linux", "ignition"}, []Field{{"a probe about ignition", 1}})
	common := c.ScoreAnalyzed([]string{"linux", "ignition"}, []Field{{"a probe about linux", 1}})
	if rare <= common {
		t.Fatalf("rare-term probe %v should beat common-term probe %v", rare, common)
	}
}

func TestUnfittedUniformIDF(t *testing.T) {
	c := Unfitted(CodeOptions())
	hit := c.ScoreAnalyzed([]string{"handler"}, []Field{{"handler.go", 3}, {"the handler", 1}})
	miss := c.ScoreAnalyzed([]string{"handler"}, []Field{{"other.go", 3}, {"nothing", 1}})
	if hit <= 0 {
		t.Fatalf("unfitted corpus should still score a present term: %v", hit)
	}
	if miss != 0 {
		t.Fatalf("unfitted corpus should score an absent term 0: %v", miss)
	}
}

func TestFieldScoresEmptyQueryIsNoOp(t *testing.T) {
	docs := [][]Field{{{"a.go", 3}}, {{"b.go", 3}}}
	for _, q := range []string{"", "  ", "a"} {
		scores := FieldScores(q, docs, CodeOptions())
		for i, s := range scores {
			if s != 0 {
				t.Fatalf("query %q should score zero, got %v at %d", q, s, i)
			}
		}
	}
}
