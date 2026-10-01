package llm

import (
	"strings"
	"testing"
)

func TestCurationFocusCacheKeyExcludesTask(t *testing.T) {
	a := CurationFocus{Tool: "read", View: "outline", Target: "a.go", Task: "one"}
	b := CurationFocus{Tool: "read", View: "outline", Target: "a.go", Task: "two"}
	if a.CacheKey() != b.CacheKey() {
		t.Fatalf("cache key should ignore task: %q vs %q", a.CacheKey(), b.CacheKey())
	}
}

func TestCurationFocusPromptFocusIncludesTask(t *testing.T) {
	f := CurationFocus{Tool: "read", View: "outline", Target: "pkg/a.go", Task: "audit altitude"}
	got := f.PromptFocus()
	for _, want := range []string{"read outline: pkg/a.go", "Task: audit altitude"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt focus = %q want substring %q", got, want)
		}
	}
}
