package grammars

import (
	"fmt"
	"runtime"
	"testing"
)

// languageMemoryCeilingBytes is the maximum HeapAlloc growth permitted for a
// single Language() call. It is measured across a fresh decode -- the
// embedded-language cache is purged before and after each language so an
// earlier language in the sweep cannot mask (or be blamed for) a later one's
// cost.
//
// 256 MB is comfortably above every non-exceptional grammar's cost (as of
// v0.43.1 the largest non-exceptional grammar's Language() call allocates
// well under 64 MB) while still being low enough to catch an accidental
// future blowup -- e.g. a new grammar whose lexer DFA repeats Swift's
// per-lex-mode fanout (see languageMemoryCeilingKnownExceptions).
const languageMemoryCeilingBytes = 256 * 1024 * 1024

// languageMemoryCeilingKnownExceptions documents grammars whose Language()
// call currently exceeds languageMemoryCeilingBytes, with the root cause and
// what fixing it for real requires. This is a v0.43.1 fast-follow finding,
// not swept under the rug: do not add entries here casually, and do not grow
// an entry's budget without also recording why. Each entry left here is a
// real, un-gated regression risk for that one language.
//
// Swift was the only entry (~490 MB HeapAlloc, ~331 lex modes rebuilding a
// shared ~190-state automaton from scratch with no cross-mode sharing). The
// fix: grammargen's buildLexDFA (grammargen/dfa.go) now runs
// minimizeLexStates (grammargen/dfa_minimize.go) after construction, a
// Myhill-Nerode partition refinement that merges observationally-equivalent
// LexStates across lex-mode boundaries. Swift's LexStates table dropped from
// 63,150 to 2,067 and its Language() call now retains about 25 MB, well
// under the ceiling; swift.bin was regenerated and recertified against the
// Swift regression suite and corpus. The map is empty until the next
// exception is found.
var languageMemoryCeilingKnownExceptions = map[string]string{}

// evaluateLanguageMemoryCeiling applies the gate's pass/known-exception/fail
// decision to a single (name, delta) measurement. Split out from the sweep
// test so the decision logic itself has a direct, fast unit test independent
// of any real grammar's actual size (see
// TestEvaluateLanguageMemoryCeilingDecisionLogic).
func evaluateLanguageMemoryCeiling(name string, deltaBytes int64) (fail bool, message string) {
	if deltaBytes < 0 {
		deltaBytes = 0
	}
	if deltaBytes <= languageMemoryCeilingBytes {
		return false, ""
	}
	megabytes := float64(deltaBytes) / (1024 * 1024)
	ceilingMB := languageMemoryCeilingBytes / (1024 * 1024)
	if reason, known := languageMemoryCeilingKnownExceptions[name]; known {
		return false, fmt.Sprintf("%s: Language() retained %.1f MB (> %d MB ceiling), known exception: %s",
			name, megabytes, ceilingMB, reason)
	}
	return true, fmt.Sprintf("%s: Language() retained %.1f MB, exceeding the %d MB ceiling and not in "+
		"languageMemoryCeilingKnownExceptions -- this is a new memory regression",
		name, megabytes, ceilingMB)
}

// TestLanguageMemoryCeilingAcrossAllGrammars sweeps every registered
// grammar's Language() call and fails if it retains more than
// languageMemoryCeilingBytes of heap, unless the language is listed in
// languageMemoryCeilingKnownExceptions. This is the regression gate for the
// v0.43.1 Swift memory finding: it does not require Swift to be fixed, but
// it does require every other grammar to stay bounded, and it requires any
// future exception to be added deliberately (with a reason) rather than
// silently.
func TestLanguageMemoryCeilingAcrossAllGrammars(t *testing.T) {
	entries := AllLanguages()
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	for _, entry := range entries {
		entry := entry
		t.Run(entry.Name, func(t *testing.T) {
			PurgeEmbeddedLanguageCache()
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			lang := entry.Language()
			if lang == nil {
				t.Fatalf("%s: Language() returned nil", entry.Name)
			}

			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			if fail, msg := evaluateLanguageMemoryCeiling(entry.Name, delta); msg != "" {
				if fail {
					t.Fatal(msg)
				}
				t.Log(msg)
			}

			UnloadEmbeddedLanguage(entry.Name + ".bin")
		})
	}
}

// TestEvaluateLanguageMemoryCeilingDecisionLogic exercises the gate's
// pass/known-exception/fail branches directly with synthetic deltas, so the
// control flow is verified without depending on any real grammar's current
// size (which could drift and silently stop exercising the fail branch).
func TestEvaluateLanguageMemoryCeilingDecisionLogic(t *testing.T) {
	if _, known := languageMemoryCeilingKnownExceptions["kotlin"]; known {
		t.Fatal("test fixture assumes \"kotlin\" is not a known exception")
	}

	t.Run("under ceiling passes silently", func(t *testing.T) {
		fail, msg := evaluateLanguageMemoryCeiling("kotlin", 10*1024*1024)
		if fail || msg != "" {
			t.Fatalf("fail=%v msg=%q, want fail=false msg=\"\"", fail, msg)
		}
	})

	t.Run("over ceiling with no exception fails", func(t *testing.T) {
		fail, msg := evaluateLanguageMemoryCeiling("kotlin", languageMemoryCeilingBytes+1)
		if !fail || msg == "" {
			t.Fatalf("fail=%v msg=%q, want fail=true with a non-empty message", fail, msg)
		}
	})

	t.Run("over ceiling with a known exception logs, does not fail", func(t *testing.T) {
		// Inject a throwaway fixture key instead of depending on a real
		// grammar's exception entry: languageMemoryCeilingKnownExceptions is
		// meant to shrink to empty over time (Swift's entry was removed once
		// grammargen's lex-state minimization brought it under the ceiling),
		// so this branch must stay exercisable even when the map is empty.
		const fixtureName = "__test_fixture_known_exception__"
		languageMemoryCeilingKnownExceptions[fixtureName] = "synthetic fixture reason"
		t.Cleanup(func() { delete(languageMemoryCeilingKnownExceptions, fixtureName) })

		fail, msg := evaluateLanguageMemoryCeiling(fixtureName, languageMemoryCeilingBytes+1)
		if fail || msg == "" {
			t.Fatalf("fail=%v msg=%q, want fail=false with a non-empty (logged) message", fail, msg)
		}
	})

	t.Run("exactly at ceiling passes", func(t *testing.T) {
		fail, msg := evaluateLanguageMemoryCeiling("kotlin", languageMemoryCeilingBytes)
		if fail || msg != "" {
			t.Fatalf("fail=%v msg=%q, want fail=false msg=\"\" at the exact ceiling", fail, msg)
		}
	})
}
