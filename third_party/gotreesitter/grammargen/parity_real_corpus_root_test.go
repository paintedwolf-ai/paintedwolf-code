package grammargen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemapParityCorpusPath(t *testing.T) {
	cases := []struct{ path, root, want string }{
		{"/tmp/grammar_parity/json/src/grammar.json", "", "/tmp/grammar_parity/json/src/grammar.json"},
		{"/tmp/grammar_parity/json/src/grammar.json", "/tmp/grammar_parity", "/tmp/grammar_parity/json/src/grammar.json"},
		{"/tmp/grammar_parity/json/src/grammar.json", "/durable/corpora", "/durable/corpora/json/src/grammar.json"},
		{"/tmp/grammar_parity/php/php/src/grammar.json", "/durable/corpora", "/durable/corpora/php/php/src/grammar.json"},
		{"/elsewhere/grammar.json", "/durable/corpora", "/elsewhere/grammar.json"},
		{"", "/durable/corpora", ""},
	}
	for _, c := range cases {
		if got := remapParityCorpusPath(c.path, c.root); got != c.want {
			t.Errorf("remapParityCorpusPath(%q, %q) = %q, want %q", c.path, c.root, got, c.want)
		}
	}
}

func TestParityGrammarRepoRootCustomRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "json", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := importParityGrammar{
		name:     "json",
		jsonPath: "/tmp/grammar_parity/json/src/grammar.json",
	}
	got := parityGrammarRepoRoot(g, root)
	want := filepath.Join(root, "json")
	if got != want {
		t.Errorf("parityGrammarRepoRoot custom root = %q, want %q", got, want)
	}
}

func TestCommittedRealCorpusFloorsRespectEligibleCap(t *testing.T) {
	floors, found, err := loadRealCorpusFloorFile(defaultRealCorpusFloorsPath())
	if err != nil {
		t.Fatalf("load committed real-corpus floors: %v", err)
	}
	if !found {
		t.Fatal("committed real-corpus floors are missing")
	}
	if floors.MaxCases <= 0 {
		t.Fatalf("committed max_cases = %d, want a positive cap", floors.MaxCases)
	}
	if got := defaultMaxCasesForProfile(realCorpusProfileAggressive); got != floors.MaxCases {
		t.Fatalf("aggressive default max cases = %d, committed floor uses %d", got, floors.MaxCases)
	}
}

func TestLoadRealCorpusFloorsRejectsEligibleAboveCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	data := []byte(`{
  "version": 3,
  "max_cases": 2,
  "metrics": {
    "example": {"eligible": 3, "no_error": 1, "sexpr_parity": 1, "deep_parity": 1}
  }
}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write invalid floor fixture: %v", err)
	}
	if _, _, err := loadRealCorpusFloorFile(path); err == nil {
		t.Fatal("loadRealCorpusFloorFile accepted eligible count above max_cases")
	}
}
