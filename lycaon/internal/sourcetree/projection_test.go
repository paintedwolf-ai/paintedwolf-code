package sourcetree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectionUsesCompactIntentAndWeightedChildren(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := sourcecatalog.Root{ID: "root", Path: t.TempDir()}
	for _, name := range []string{"a/deep/one.txt", "a/two.txt", "b/three.txt", "top.txt"} {
		file := filepath.Join(root.Path, name)
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(file), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(file, []byte("content"), 0o600))
	}
	catalog := sourcecatalog.New()
	defer func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) }()
	for _, dir := range []string{".", "a", "a/deep", "b"} {
		_, err := catalog.ObserveDirectory(t.Context(), "project", root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe directory", err)
	}
	navigation, err := catalog.OpenNavigation(t.Context(), "project", root)
	testutil.FailErr(t, "pin navigation", err)
	defer func() { _ = navigation.Close() }()
	rules := &Rules{}
	projection := Projection{Root: "root", Navigation: navigation, Rules: rules}
	rows, total, err := projection.Frame(t.Context(), 0, 200)
	testutil.FailErr(t, "closed frame", err)
	if total != 4 || len(rows) != 4 {
		t.Fatalf("closed root: total %d rows %+v", total, rows)
	}
	rules.Set(Address{Root: "root", Path: "."}, Disclosure{Open: true, Recursive: true})
	rows, total, err = projection.Frame(t.Context(), 0, 200)
	testutil.FailErr(t, "expanded frame", err)
	if total != 8 || len(rows) != 8 || rows[3].Address.Path != "a/deep/one.txt" {
		t.Fatalf("expanded root: total %d rows %+v", total, rows)
	}
	rules.Set(Address{Root: "root", Path: "a"}, Disclosure{Open: false})
	rows, total, err = projection.Frame(t.Context(), 0, 200)
	testutil.FailErr(t, "subtree override", err)
	if total != 5 || len(rows) != 5 || rows[2].Address.Path != "b" {
		t.Fatalf("collapsed subtree: total %d rows %+v", total, rows)
	}
	if rules.count() != 2 {
		t.Fatal("expansion materialized directory flags")
	}
}
