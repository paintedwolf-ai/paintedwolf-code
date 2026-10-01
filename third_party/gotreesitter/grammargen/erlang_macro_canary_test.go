package grammargen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const erlangMacroConcatCanary = "-define(TXT(Str), \"abc\" Str ??Str).\n" +
	"-define(TXT(Str), Str \"abc\").\n" +
	"-define(TXT(Str), ??Str \"abc\").\n" +
	"-define(TXT(Str), \"abc\" \"def\").\n"

func TestGeneratedErlangMacroConcatCanaryMatchesReference(t *testing.T) {
	genLang, refLang := loadGeneratedErlangLanguageForParity(t)
	assertGeneratedAndReferenceDeepParity(t, genLang, refLang, erlangMacroConcatCanary)
	assertGeneratedErlangMacroConcatExposesConcatables(t, genLang)
}

func TestGeneratedErlangMacroConcatRealCorpusCanaryMatchesReference(t *testing.T) {
	genLang, refLang := loadGeneratedErlangLanguageForParity(t)

	repoRoot := "/tmp/grammar_parity/erlang"
	if info, err := os.Stat(repoRoot); err != nil || !info.IsDir() {
		t.Skipf("erlang corpus repo not available: %v", err)
	}
	candidates := collectGrammarCorpusCandidates(t, repoRoot, realCorpusCollectConfig{
		Profile:             realCorpusProfileAggressive,
		TargetEligible:      25,
		MaxSampleBytes:      defaultMaxSampleBytesForProfile(realCorpusProfileAggressive),
		CandidateMultiplier: defaultCandidateMultiplierForProfile(realCorpusProfileAggressive),
	})
	target := -1
	for i, cand := range candidates {
		if cand.Source != realCorpusSourceCorpusBlock ||
			!strings.HasSuffix(filepath.ToSlash(cand.Path), "/test/corpus/macros.txt") ||
			!strings.Contains(cand.Text, `-define(TXT(Str), Str "abc").`) {
			continue
		}
		target = i
		break
	}
	if target < 0 {
		t.Skip("erlang concatables corpus block not found")
	}

	genParser := gotreesitter.NewParser(genLang)
	refParser := gotreesitter.NewParser(refLang)
	for i := 0; i <= target; i++ {
		data := []byte(candidates[i].Text)
		genTree, err := genParser.Parse(data)
		if err != nil {
			t.Fatalf("generated parse sample %d: %v", i, err)
		}
		refTree, err := refParser.Parse(data)
		if err != nil {
			genTree.Release()
			t.Fatalf("reference parse sample %d: %v", i, err)
		}
		if i == target {
			genRoot := genTree.RootNode()
			refRoot := refTree.RootNode()
			divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 10)
			if len(divs) > 0 {
				t.Fatalf("deep mismatch after real-corpus parser reuse\nGEN: %s\nREF: %s\nDIVS: %v",
					safeSExpr(genRoot, genLang, 256),
					safeSExpr(refRoot, refLang, 256),
					divs)
			}
			assertErlangMacroConcatTreeExposesConcatables(t, genRoot, genLang)
		}
		genTree.Release()
		refTree.Release()
	}
}

func assertGeneratedErlangMacroConcatExposesConcatables(t *testing.T, lang *gotreesitter.Language) {
	t.Helper()

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte("-define(TXT(Str), Str \"abc\").\n"))
	if err != nil {
		t.Fatalf("generated macro concat parse: %v", err)
	}
	defer tree.Release()
	assertErlangMacroConcatTreeExposesConcatables(t, tree.RootNode(), lang)
}

func assertErlangMacroConcatTreeExposesConcatables(t *testing.T, root *gotreesitter.Node, lang *gotreesitter.Language) {
	t.Helper()

	sexpr := root.SExpr(lang)
	if !strings.Contains(sexpr, "(pp_define") || !strings.Contains(sexpr, "(concatables") {
		t.Fatalf("macro concat canary did not expose concatables under pp_define: %s", safeSExpr(root, lang, 256))
	}
	if strings.Contains(sexpr, "(replacement_guard_or") || strings.Contains(sexpr, "(replacement_guard_and") {
		t.Fatalf("macro concat canary exposed guard wrapper instead of concatables: %s", safeSExpr(root, lang, 256))
	}
}

func loadGeneratedErlangLanguageForParity(t *testing.T) (*gotreesitter.Language, *gotreesitter.Language) {
	t.Helper()

	source, err := os.ReadFile("/tmp/grammar_parity/erlang/src/grammar.json")
	if err != nil {
		t.Skipf("erlang grammar.json not available: %v", err)
	}
	gram, err := ImportGrammarJSON(source)
	if err != nil {
		t.Fatalf("ImportGrammarJSON(erlang): %v", err)
	}
	genLang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("GenerateLanguage(erlang): %v", err)
	}
	refLang := grammars.ErlangLanguage()
	adaptExternalScanner(refLang, genLang)
	return genLang, refLang
}
