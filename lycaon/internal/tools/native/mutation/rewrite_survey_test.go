package mutation

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCodeRewriteRecursiveSurveyPreservesCaptureAndPublication(t *testing.T) {
	const before = "package p\n\nfunc f() int { return 1 + 2 }\n"
	for _, dryRun := range []bool{false, true} {
		name := "apply"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"tree/keep.go":                  before,
				"tree/sub/keep.go":              before,
				"tree/ignored.go":               before,
				"tree/.gitignore":               "ignored.go\n",
				"tree/.git/config.go":           before,
				"tree/.paintedwolf/settings.go": before,
				"tree/.hidden.go":               before,
				"tree/readme.txt":               "1 + 2\n",
			})
			words := []string{"code-rewrite", "--path", "tree", "--recursive", "--pattern", "1 + 2", "--rewrite", "3"}
			if dryRun {
				words = append(words, "--dry-run")
			}
			result := h.run("", words...)
			if result.err != nil || result.result.Status != 0 {
				t.Fatalf("rewrite %+v %v", result.result, result.err)
			}
			effects := toolcontract.EffectCompleted
			if dryRun {
				effects = toolcontract.NoEffect
			}
			if result.result.EffectCertainty != effects {
				t.Fatalf("effects %+v", result.result)
			}
			expected := map[string]bool{"tree/keep.go": false, "tree/sub/keep.go": false, "tree/ignored.go": false}
			for path := range expected {
				text := h.read(path)
				if dryRun && text != before || !dryRun && (!strings.Contains(text, "return 3") || strings.Contains(text, "1 + 2")) {
					t.Fatalf("%s: %q", path, text)
				}
			}
			for _, path := range []string{"tree/.git/config.go", "tree/.paintedwolf/settings.go", "tree/.hidden.go"} {
				if text := h.read(path); text != before {
					t.Fatalf("pruned source %s changed: %q", path, text)
				}
			}
			if text := h.read("tree/readme.txt"); text != "1 + 2\n" {
				t.Fatalf("unsupported source changed: %q", text)
			}
			for _, pin := range h.pins.pins[result.execution] {
				if _, ok := expected[pin.Path]; !ok {
					t.Fatalf("captured an excluded source: %+v", pin)
				}
				expected[pin.Path] = true
			}
			for path, captured := range expected {
				if !captured {
					t.Fatalf("no source capture for %s", path)
				}
			}
		})
	}
}

func TestEditIdenticalReplacementSuppliesConflictFactsWithoutEffects(t *testing.T) {
	h := newHarness(t, map[string]string{"file.txt": "unchanged\n"})
	result := h.run("", "edit", "--path", "file.txt", "--old-string", "unchanged", "--new-string", "unchanged")
	reject := toolrejection.AsToolReject(result.err)
	if reject == nil || reject.Code != "EDIT_ARGS_CONFLICT" {
		t.Fatalf("edit conflict %+v %v", result.result, result.err)
	}
	detail, ok := reject.Data["detail"].(string)
	if !ok || detail == "" || reject.Data["conflict"] != true {
		t.Fatalf("conflict facts %+v", reject.Data)
	}
	if result.result.EffectCertainty != toolcontract.NoEffect || len(h.pins.pins[result.execution]) != 0 || h.read("file.txt") != "unchanged\n" {
		t.Fatalf("conflicting edit reached source capture or publication: %+v", result.result)
	}
}
