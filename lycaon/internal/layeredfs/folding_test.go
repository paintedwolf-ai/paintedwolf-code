package layeredfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/text/unicode/norm"
)

func newFoldingCore(t *testing.T, folding NameFolding, seed map[string]string) *Core {
	t.Helper()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	for rel, content := range seed {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, filepath.Dir(rel)), 0o755))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(trunk, rel), []byte(content), 0o644))
	}
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))
	core, err := New(Config{
		ViewID: "view-folding", BackingDir: backing,
		Roots:        []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
		FoldingProbe: func(string) NameFolding { return folding },
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	return core
}

func primary(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }

func listedNames(t *testing.T, c *Core, dir string) []string {
	t.Helper()
	entries, err := c.ReadDir(primary(dir))
	testutil.FailErr(t, "ReadDir "+dir, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

var foldsBoth = NameFolding{Case: true, Unicode: true}

// On a folding root every spelling of a private name gives the private result, and a listing shows
// one entry under the spelling the worker last gave.
func TestFoldingRootSeesOnePrivateNameUnderEverySpelling(t *testing.T) {
	t.Parallel()
	c := newFoldingCore(t, foldsBoth, map[string]string{"README.md": "trunk\n", "docs/Guide.md": "guide\n"})

	_, err := c.WriteFile(primary("README.md"), []byte("edited\n"), 0o644)
	testutil.FailErr(t, "edit", err)
	for _, spelling := range []string{"readme.md", "ReadMe.MD", "README.md"} {
		got, err := c.ReadFile(primary(spelling))
		testutil.FailErr(t, "read "+spelling, err)
		if string(got) != "edited\n" {
			t.Fatalf("%s = %q, want the private edit", spelling, got)
		}
	}

	_, err = c.WriteFile(primary("New.txt"), []byte("new\n"), 0o644)
	testutil.FailErr(t, "create", err)
	if _, err := c.Stat(primary("new.TXT")); err != nil {
		t.Fatalf("a new file is missing under another spelling: %v", err)
	}
	if _, _, err := c.CreateFile(primary("NEW.txt"), 0o644, true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("an exclusive create under another spelling = %v, want exists", err)
	}

	testutil.FailErr(t, "delete under another spelling", c.Unlink(primary("DOCS/guide.MD"), false))
	if _, err := c.Stat(primary("docs/Guide.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a deleted trunk file came back under its own spelling: %v", err)
	}
	if names := listedNames(t, c, "docs"); len(names) != 0 {
		t.Fatalf("docs lists %v after its only file was deleted", names)
	}
	names := listedNames(t, c, ".")
	slices.Sort(names)
	if !slices.Equal(names, []string{"New.txt", "README.md", "docs"}) {
		t.Fatalf("root lists %v", names)
	}
}

// A case-only rename is a rename: the old spelling is gone and the new one holds the content.
func TestFoldingRootRecordsACaseOnlyRename(t *testing.T) {
	t.Parallel()
	c := newFoldingCore(t, foldsBoth, map[string]string{"Makefile": "all:\n"})
	testutil.FailErr(t, "case-only rename", c.Rename(primary("Makefile"), primary("makefile")))
	if names := listedNames(t, c, "."); !slices.Equal(names, []string{"makefile"}) {
		t.Fatalf("root lists %v, want the new spelling alone", names)
	}
	got, err := c.ReadFile(primary("MAKEFILE"))
	testutil.FailErr(t, "read renamed", err)
	if string(got) != "all:\n" {
		t.Fatalf("renamed content = %q", got)
	}
	var renamed bool
	for _, op := range c.JournalOperations() {
		renamed = renamed || (op.Kind == JournalOpRename && op.Path.Rel == "makefile")
	}
	if !renamed {
		t.Fatal("the case-only rename left no rename in the journal")
	}
}

// Composed and decomposed forms of a name are one name on a root that folds Unicode.
func TestFoldingRootMatchesComposedAndDecomposedNames(t *testing.T) {
	t.Parallel()
	composed := norm.NFC.String("café.txt")
	decomposed := norm.NFD.String("café.txt")
	c := newFoldingCore(t, foldsBoth, nil)
	_, err := c.WriteFile(primary(composed), []byte("crème\n"), 0o644)
	testutil.FailErr(t, "write composed", err)
	got, err := c.ReadFile(primary(decomposed))
	testutil.FailErr(t, "read decomposed", err)
	if string(got) != "crème\n" {
		t.Fatalf("decomposed spelling read %q", got)
	}
}

// On a case-sensitive root, names that differ only by case stay two entries.
func TestCaseSensitiveRootKeepsNamesDistinct(t *testing.T) {
	t.Parallel()
	c := newFoldingCore(t, NameFolding{}, nil)
	for _, name := range []string{"a.txt", "A.txt"} {
		_, err := c.WriteFile(primary(name), []byte(name), 0o644)
		testutil.FailErr(t, "write "+name, err)
	}
	if names := listedNames(t, c, "."); !slices.Equal(names, []string{"A.txt", "a.txt"}) {
		t.Fatalf("root lists %v", names)
	}
}
