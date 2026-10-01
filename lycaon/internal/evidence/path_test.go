package evidence_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNormalizeLedgerPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  pkg/a.go  ", "pkg/a.go"},
		{`pkg\a.go`, "pkg/a.go"},
		{`.\src\internal\foo`, "src/internal/foo"},
		{"./pkg/a.go", "pkg/a.go"},
		{`src\internal`, "src/internal"},
	}
	for _, tc := range cases {
		if got := evidence.NormalizeLedgerPath(tc.in); got != tc.want {
			t.Fatalf("NormalizeLedgerPath(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestLedgerPathToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pkg/a.go", "pkg/a.go"},
		{`pkg\a.go`, "pkg/a.go"},
		{`src\internal`, "src/internal"},
		{"a.go", "a.go"},
		{"read#2", ""},
		{"README", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := evidence.LedgerPathToken(tc.in); got != tc.want {
			t.Fatalf("LedgerPathToken(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveCitationAbsAllowsDotsInsideFilename(t *testing.T) {
	dir := t.TempDir()
	name := "notes..draft.md"
	testutil.FailErr(t, "write dotted file", os.WriteFile(filepath.Join(dir, name), []byte("draft"), 0o644))
	wantAbs, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	testutil.FailErr(t, "resolve dotted file", err)
	abs, rel, ok := evidence.ResolveCitationAbs(dir, name)
	if !ok || rel != name || abs != wantAbs {
		t.Fatalf("ResolveCitationAbs = (%q, %q, %v)", abs, rel, ok)
	}
	if _, _, ok := evidence.ResolveCitationAbs(dir, "../outside"); ok {
		t.Fatal("parent traversal accepted")
	}
	if got := evidence.NormalizeResolvePath("", name); got != name {
		t.Fatalf("NormalizeResolvePath = %q want %q", got, name)
	}
}

func TestIsOpenablePath(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	testutil.FailErr(t, "MkdirAll src", os.MkdirAll(srcDir, 0o755))
	testutil.FailErr(t, "WriteFile a.go", os.WriteFile(filepath.Join(srcDir, "a.go"), []byte("package src\n"), 0o644))

	other := t.TempDir()
	otherSrc := filepath.Join(other, "lib", "b.go")
	testutil.FailErr(t, "MkdirAll lib", os.MkdirAll(filepath.Dir(otherSrc), 0o755))
	testutil.FailErr(t, "WriteFile b.go", os.WriteFile(otherSrc, []byte("package lib\n"), 0o644))
	multiRoot := evidence.CitationRoots{
		Roots: []projectroot.RootRef{
			{ID: "primary", Path: dir, IsPrimary: true},
			{ID: "other", Path: other},
		},
		ActiveRootID: "primary",
		ProjectDir:   dir,
	}

	cases := []struct {
		name      string
		roots     evidence.CitationRoots
		pathQuery string
		want      *bool
	}{
		{name: "empty roots", roots: evidence.CitationRoots{}, pathQuery: "src/a.go", want: nil},
		{name: "empty path", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: "  ", want: nil},
		{name: "regular file", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: "src/a.go", want: boolPtr(true)},
		{name: "windows separators", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: `src\a.go`, want: boolPtr(true)},
		{name: "directory", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: "src", want: boolPtr(false)},
		{name: "missing path", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: "src/missing.go", want: boolPtr(false)},
		{name: "outside jail", roots: evidence.CitationRoots{ProjectDir: dir}, pathQuery: "../etc/passwd", want: boolPtr(false)},
		{name: "file under non-primary root", roots: multiRoot, pathQuery: "lib/b.go", want: boolPtr(true)},
		{name: "absent from every root", roots: multiRoot, pathQuery: "nope.go", want: boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evidence.IsOpenablePath(tc.roots, tc.pathQuery)
			if !sameBoolPtr(got, tc.want) {
				t.Fatalf("IsOpenablePath(%v, %q) = %s want %s", tc.roots, tc.pathQuery, boolPtrStr(got), boolPtrStr(tc.want))
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func boolPtrStr(p *bool) string {
	if p == nil {
		return "nil"
	}
	if *p {
		return "true"
	}
	return "false"
}
