package projectroot

import (
	"path/filepath"
	"strings"
	"testing"
)

func rewriteTestRoots(t *testing.T) ([]RootRef, string, string) {
	t.Helper()
	dir := t.TempDir()
	primary := filepath.Join(dir, "lycaon")
	secondary := filepath.Join(dir, "lycaon-den")
	roots := []RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	return roots, primary, secondary
}

func TestRewriteAttachedRootPathsKeysOnActiveRoot(t *testing.T) {
	roots, primary, secondary := rewriteTestRoots(t)
	cases := []struct {
		active string
		in     string
		want   string
	}{
		// Active root paths render bare.
		{"p", "wrote " + primary + "/src/a.go", "wrote src/a.go"},
		{"p", primary + " done", ". done"},
		{"p", "cd " + primary + " && ls", "cd . && ls"},
		// Secondary root paths keep the @label prefix.
		{"p", "wrote " + secondary + "/src/b.go", "wrote @lycaon-den/src/b.go"},
		{"d", "wrote " + secondary + "/src/b.go", "wrote src/b.go"},
		{"d", "wrote " + primary + "/src/a.go", "wrote @lycaon/src/a.go"},
		// Quoted and parenthesized spellings rewrite too.
		{"p", `see "` + primary + `/x.txt"`, `see "x.txt"`},
		{"p", "diff (" + primary + "/a) vs (" + secondary + "/b)", "diff (a) vs (@lycaon-den/b)"},
		// Grep-style line suffixes stay attached to the rewritten path.
		{"p", primary + "/src/a.go:12:5: body", "src/a.go:12:5: body"},
		// Substrings that merely contain a root path are left alone.
		{"p", "prefix" + primary + "/x", "prefix" + primary + "/x"},
		{"p", primary + "-suffix", primary + "-suffix"},
		{"p", "no roots here", "no roots here"},
		{"p", "", ""},
		// Paths outside every attached root are untouched.
		{"p", "/elsewhere/src/a.go", "/elsewhere/src/a.go"},
	}
	for _, tc := range cases {
		if got := RewriteAttachedRootPaths(roots, tc.active, tc.in); got != tc.want {
			t.Fatalf("RewriteAttachedRootPaths(active=%q, %q) = %q want %q", tc.active, tc.in, got, tc.want)
		}
	}
}

func TestRewriteAttachedRootPathsEmptyRoots(t *testing.T) {
	in := "/some/abs/path a " + "/other"
	if got := RewriteAttachedRootPaths(nil, "", in); got != in {
		t.Fatalf("RewriteAttachedRootPaths(nil) = %q want %q", got, in)
	}
}

func TestRewriteAttachedRootPathsRewritesEveryOccurrence(t *testing.T) {
	roots, primary, _ := rewriteTestRoots(t)
	in := primary + "/a " + primary + "/b " + primary
	want := "a b ."
	if got := RewriteAttachedRootPaths(roots, "p", in); got != want {
		t.Fatalf("RewriteAttachedRootPaths = %q want %q", got, want)
	}
}

func TestRewriteAttachedRootPathsNestedRootWins(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "outer")
	inner := filepath.Join(outer, "inner")
	roots := []RootRef{
		{ID: "o", Label: "outer", Path: outer, IsPrimary: true},
		{ID: "i", Label: "inner", Path: inner, IsPrimary: false},
	}
	in := "found " + inner + "/file.txt"
	want := "found @inner/file.txt"
	if got := RewriteAttachedRootPaths(roots, "o", in); got != want {
		t.Fatalf("RewriteAttachedRootPaths = %q want %q", got, want)
	}
	if got := RewriteAttachedRootPaths(roots, "i", in); got != "found file.txt" {
		t.Fatalf("RewriteAttachedRootPaths(active=inner) = %q", got)
	}
}

func TestRewriteAttachedRootPathsDoesNotLoop(t *testing.T) {
	roots, primary, _ := rewriteTestRoots(t)
	in := strings.Repeat(primary+"/x ", 200)
	got := RewriteAttachedRootPaths(roots, "p", in)
	if strings.Contains(got, primary) {
		t.Fatal("rewrite left an absolute root path behind")
	}
}
