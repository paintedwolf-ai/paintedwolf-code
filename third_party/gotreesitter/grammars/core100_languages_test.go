package grammars

import "testing"

func TestCore100LanguageNames(t *testing.T) {
	names := Core100LanguageNames()
	if len(names) != 100 {
		t.Fatalf("expected Core100 length 100, got %d", len(names))
	}

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, dup := seen[name]; dup {
			t.Fatalf("duplicate Core100 language %q", name)
		}
		seen[name] = struct{}{}
	}

	entries := AllLanguages()
	entryByName := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name] = struct{}{}
	}

	for _, name := range names {
		if _, ok := entryByName[name]; !ok {
			t.Fatalf("Core100 language %q is not registered", name)
		}
	}
}
