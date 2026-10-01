package sandbox

import (
	"testing"
)

func TestValidScopeTokenValueRejectsUnsafe(t *testing.T) {
	unsafe := []string{
		"",
		"   ",
		"..",
		"a/b",
		`a\b`,
		"a*",
		"a?",
		"a[1]",
		"a]b",
	}
	for _, v := range unsafe {
		if ValidScopeTokenValue(v) {
			t.Fatalf("expected %q rejected", v)
		}
	}
}

func TestValidScopeTokenValueAcceptsSafe(t *testing.T) {
	safe := []string{
		"coordinator",
		"job-abc-123",
		"implementer",
		"worker_1",
		"a..b",
	}
	for _, v := range safe {
		if !ValidScopeTokenValue(v) {
			t.Fatalf("expected %q accepted", v)
		}
	}
}

func TestSubstituteScopeGlobsDropsUnsafeTokenPatterns(t *testing.T) {
	const stablePattern = "src/**"
	patterns := []string{
		"work/{job}/**",
		stablePattern,
	}
	tokens := ScopeTokens{Job: ".."}
	out := SubstituteScopeGlobs(patterns, tokens)
	if len(out) != 1 || out[0] != stablePattern {
		t.Fatalf("globs = %v want stable pattern only", out)
	}
}

func TestSubstituteScopeGlobsRejectsSelfGlobMeta(t *testing.T) {
	out := SubstituteScopeGlobs([]string{"work/{self}/**"}, ScopeTokens{Self: "a*b"})
	if len(out) != 0 {
		t.Fatalf("globs = %v want none", out)
	}
}

func TestSubstituteScopeGlobsExpandsJobToken(t *testing.T) {
	out := SubstituteScopeGlobs([]string{"work/{job}/**"}, ScopeTokens{Job: "job-1"})
	want := "work/job-1/**"
	if len(out) != 1 || out[0] != want {
		t.Fatalf("globs = %v want %q", out, want)
	}
}

func TestSubstituteScopeGlobsExpandsSelfToken(t *testing.T) {
	out := SubstituteScopeGlobs([]string{"agents/{self}/notes.md"}, ScopeTokens{Self: "path-explorer"})
	want := "agents/path-explorer/notes.md"
	if len(out) != 1 || out[0] != want {
		t.Fatalf("globs = %v want %q", out, want)
	}
}

func TestSubstituteScopeGlobsPassthroughWithoutTokens(t *testing.T) {
	patterns := []string{"src/**", "docs/**"}
	out := SubstituteScopeGlobs(patterns, ScopeTokens{})
	if len(out) != 2 {
		t.Fatalf("globs = %v", out)
	}
}
