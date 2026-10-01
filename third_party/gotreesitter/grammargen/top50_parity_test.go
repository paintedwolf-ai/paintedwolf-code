package grammargen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	top50MinNoErrorFloors     = 50
	top50MinExactParityFloors = 49
)

func TestTop50GrammarImportParityCoverage(t *testing.T) {
	top50 := loadTop50GrammarLockFile(t)
	if len(top50) != 50 {
		t.Fatalf("top50 lock file contains %d languages, want 50", len(top50))
	}

	byName := make(map[string]importParityGrammar, len(importParityGrammars))
	for _, grammar := range importParityGrammars {
		if _, ok := byName[grammar.name]; ok {
			t.Fatalf("duplicate import parity grammar %q", grammar.name)
		}
		byName[grammar.name] = grammar
	}

	var problems []string
	var noErrorGaps []string
	var exactParityGaps []string
	noErrorFloors := 0
	exactParityFloors := 0
	for _, languageName := range top50 {
		parityName := top50ImportParityName(languageName)
		grammar, ok := byName[parityName]
		if !ok {
			problems = append(problems, languageName+": missing import/parity grammar "+parityName)
			continue
		}
		if !grammar.expectImport {
			problems = append(problems, languageName+": import floor is disabled")
		}
		if !grammar.expectGenerate {
			problems = append(problems, languageName+": generate floor is disabled")
		}
		if grammar.expectNoErrors > 0 {
			noErrorFloors++
		} else if grammar.expectNoErrors < 0 {
			problems = append(problems, languageName+": no-error floor is negative")
		} else {
			noErrorGaps = append(noErrorGaps, languageName)
		}
		if grammar.expectParity > 0 {
			exactParityFloors++
		} else if grammar.expectParity < 0 {
			problems = append(problems, languageName+": parity floor is negative")
		} else {
			exactParityGaps = append(exactParityGaps, languageName)
		}
		if grammar.expectNoErrors == 0 && grammar.expectParity == 0 {
			problems = append(problems, languageName+": no parse or parity floor is set")
		}
	}
	if len(problems) > 0 {
		t.Fatalf("top50 grammargen parity coverage regressions:\n%s", strings.Join(problems, "\n"))
	}
	if noErrorFloors < top50MinNoErrorFloors {
		t.Fatalf("top50 no-error floor coverage regressed: got %d/%d, floor %d/%d",
			noErrorFloors, len(top50), top50MinNoErrorFloors, len(top50))
	}
	if exactParityFloors < top50MinExactParityFloors {
		t.Fatalf("top50 exact parity floor coverage regressed: got %d/%d, floor %d/%d",
			exactParityFloors, len(top50), top50MinExactParityFloors, len(top50))
	}
	t.Logf("top50 grammargen parity coverage: %d/%d import+generate, %d/%d no-error floors, %d/%d exact parity floors",
		len(top50), len(top50), noErrorFloors, len(top50), exactParityFloors, len(top50))
	if len(noErrorGaps) > 0 {
		t.Logf("top50 no-error floor gaps: %s", strings.Join(noErrorGaps, ", "))
	}
	if len(exactParityGaps) > 0 {
		t.Logf("top50 exact parity floor gaps: %s", strings.Join(exactParityGaps, ", "))
	}
}

func loadTop50GrammarLockFile(t *testing.T) []string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate top50 parity test file")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "grammars", "update_tier1_top50.txt"))
	if err != nil {
		t.Fatalf("load top50 lock file: %v", err)
	}
	lines := strings.Split(string(source), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return names
}

func top50ImportParityName(languageName string) string {
	switch languageName {
	case "c":
		return "c_lang"
	case "go":
		return "go_lang"
	default:
		return languageName
	}
}
