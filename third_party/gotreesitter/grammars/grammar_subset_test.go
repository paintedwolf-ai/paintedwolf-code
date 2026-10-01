//go:build grammar_subset

package grammars

import (
	"os"
	"sync"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func resetSubsetRegistryStateForTests() {
	registry = nil
	highlightInheritanceResolved = false
	builtinRegistryOnce = sync.Once{}
	builtinRegistryBusy.Store(false)
	runtimeLanguageSetOnce = sync.Once{}
	runtimeLanguageSet = nil
	runtimeLanguageEnabled = false
	PurgeEmbeddedLanguageCache()
}

func restoreSubsetRegistryStateForTests(entries []LangEntry) {
	resetSubsetRegistryStateForTests()
	for _, entry := range entries {
		Register(entry)
	}
}

func configureExternalBlobDirForTests(t *testing.T) {
	t.Helper()
	if os.Getenv("GOTREESITTER_GRAMMAR_BLOB_DIR") != "" {
		return
	}
	if _, err := os.Stat("grammar_blobs"); err == nil {
		t.Setenv("GOTREESITTER_GRAMMAR_BLOB_DIR", "grammar_blobs")
	}
}

func TestGrammarSubsetRuntimeFilterCanIsolateSingleCompiledLanguage(t *testing.T) {
	entries := AllLanguages()
	if len(entries) == 0 {
		t.Fatal("expected at least one compiled grammar in grammar_subset build")
	}
	t.Cleanup(func() {
		restoreSubsetRegistryStateForTests(entries)
	})

	chosen := entries[0].Name
	t.Setenv("GOTREESITTER_GRAMMAR_SET", chosen)

	restoreSubsetRegistryStateForTests(entries)
	filtered := append([]LangEntry(nil), AllLanguages()...)
	if len(filtered) != 1 {
		t.Fatalf("runtime grammar set filter returned %d languages, want 1", len(filtered))
	}
	if filtered[0].Name != chosen {
		t.Fatalf("runtime grammar set filter kept %q, want %q", filtered[0].Name, chosen)
	}
}

func TestGrammarSubsetCanParseSmokeSampleForCompiledLanguage(t *testing.T) {
	configureExternalBlobDirForTests(t)

	entries := AllLanguages()
	if len(entries) == 0 {
		t.Fatal("expected at least one compiled grammar in grammar_subset build")
	}
	t.Cleanup(func() {
		restoreSubsetRegistryStateForTests(entries)
	})

	for _, entry := range entries {
		lang := entry.Language()
		if lang == nil {
			continue
		}
		report := EvaluateParseSupport(entry, lang)
		if report.Backend == ParseBackendUnsupported {
			continue
		}

		if len(entry.Extensions) > 0 {
			detected := DetectLanguage("subset_smoke" + entry.Extensions[0])
			if detected == nil || detected.Name != entry.Name {
				got := "<nil>"
				if detected != nil {
					got = detected.Name
				}
				t.Fatalf("DetectLanguage() = %s, want %s", got, entry.Name)
			}
		}

		source := []byte(ParseSmokeSample(entry.Name))
		parser := gotreesitter.NewParser(lang)

		var (
			tree *gotreesitter.Tree
			err  error
		)
		switch report.Backend {
		case ParseBackendTokenSource:
			tree, err = parser.ParseWithTokenSource(source, entry.TokenSourceFactory(source, lang))
		case ParseBackendDFA, ParseBackendDFAPartial:
			tree, err = parser.Parse(source)
		default:
			t.Fatalf("unexpected backend %q for %q", report.Backend, entry.Name)
		}
		if err != nil {
			t.Fatalf("parse %q smoke sample: %v", entry.Name, err)
		}
		if tree == nil || tree.RootNode() == nil {
			t.Fatalf("parse %q smoke sample returned nil tree/root", entry.Name)
		}
		tree.Release()
		return
	}

	t.Fatal("expected at least one compiled grammar to be parseable in grammar_subset build")
}
