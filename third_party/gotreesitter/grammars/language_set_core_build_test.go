//go:build grammar_set_core

package grammars

import "testing"

func TestGrammarSetCoreBuildIncludesCore100(t *testing.T) {
	names := Core100LanguageNames()
	expected := make(map[string]struct{}, len(names))
	for _, name := range names {
		expected[name] = struct{}{}
	}

	entries := AllLanguages()
	if len(entries) != len(expected) {
		t.Fatalf("grammar_set_core build expected %d languages, got %d", len(expected), len(entries))
	}

	for _, entry := range entries {
		if _, ok := expected[entry.Name]; !ok {
			t.Fatalf("grammar_set_core includes non-Core100 language %q", entry.Name)
		}
	}
}
