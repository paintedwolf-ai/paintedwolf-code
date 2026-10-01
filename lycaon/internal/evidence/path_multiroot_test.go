package evidence_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLedgerPathKey_multiRootBareMapsToQualified(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	rel := filepath.Join("lycaon", "internal", "oar", "doc.go")
	if err := os.MkdirAll(filepath.Join(secondary, "lycaon", "internal", "oar"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(secondary, rel), []byte("// Package oar\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "lycaon", Path: secondary, IsPrimary: false},
	}
	byPath := map[string][]string{
		"@lycaon/lycaon/internal/oar/doc.go": {"read#1"},
	}
	ctx := evidence.CitationRoots{Roots: roots, ActiveRootID: "p"}
	got := evidence.LedgerPathKey(ctx, "lycaon/internal/oar/doc.go", byPath)
	want := "@lycaon/lycaon/internal/oar/doc.go"
	if got != want {
		t.Fatalf("LedgerPathKey = %q want %q", got, want)
	}
	exact := evidence.LedgerPathKey(ctx, want, byPath)
	if exact != want {
		t.Fatalf("exact @label key = %q want %q", exact, want)
	}
}

func TestResolveCitationFile_labeledPath(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	if err := os.WriteFile(filepath.Join(secondary, "README.md"), []byte("# hi\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "g", Label: "oar-gateway", Path: secondary, IsPrimary: false},
	}
	abs, display, ok := evidence.ResolveCitationFile(evidence.CitationRoots{
		Roots: roots, ActiveRootID: "p",
	}, "@oar-gateway/README.md")
	if !ok {
		t.Fatal("expected labeled path to resolve")
	}
	if display != "@oar-gateway/README.md" {
		t.Fatalf("display = %q", display)
	}
	if abs != filepath.Join(secondary, "README.md") {
		t.Fatalf("abs = %q", abs)
	}
}
