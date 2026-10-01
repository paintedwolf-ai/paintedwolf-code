package repomap

import (
	"sync"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestTaggerCacheConcurrentTag(t *testing.T) {
	fixtures := []struct{ language, source string }{
		{"go", "package p\nfunc Run() {}\n"},
		{"python", "def run():\n    return 1\n"},
		{"typescript", "function run() { return 1; }\n"},
	}
	cache := &taggerCache{}
	for _, fixture := range fixtures {
		entry := grammars.DetectLanguageByName(fixture.language)
		parsed := cache.tag(t.Context(), *entry, []byte(fixture.source))
		if parsed.failure != nil || parsed.noTagger || len(parsed.tags) == 0 {
			t.Fatalf("initialize %s tagger: %+v", fixture.language, parsed)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		for _, fixture := range fixtures {
			wg.Add(1)
			go func() {
				defer wg.Done()
				entry := grammars.DetectLanguageByName(fixture.language)
				parsed := cache.tag(t.Context(), *entry, []byte(fixture.source))
				if parsed.failure != nil || parsed.noTagger || len(parsed.tags) == 0 {
					t.Errorf("concurrent %s tag: %+v", fixture.language, parsed)
				}
			}()
		}
	}
	wg.Wait()
}

func TestTaggerInitializationFailuresRemainVisible(t *testing.T) {
	entry := *grammars.DetectLanguageByName("go")
	entry.Name = "tag-query-diagnostic"
	entry.TagsQuery = "(no_such_node) @definition.function"
	cache := &taggerCache{}
	for range 2 {
		parsed := cache.tag(t.Context(), entry, []byte("package p\n"))
		if parsed.noTagger || parsed.failure == nil || parsed.failure.Reason != "tagger_configuration" || parsed.failure.Detail == "" {
			t.Fatalf("tag query failure became an unsupported grammar: %+v", parsed)
		}
	}
	entry.Name = "grammar-initialization-diagnostic"
	entry.Language = func() *gotreesitter.Language { panic("grammar initialization failed") }
	parsed := cache.tag(t.Context(), entry, []byte("package p\n"))
	if parsed.failure == nil || parsed.failure.Reason != "panic" || parsed.failure.SourceBytes != 10 || parsed.failure.Detail == "" {
		t.Fatalf("grammar panic lost its source diagnostics: %+v", parsed)
	}
}
