package sourcetree

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func reviewFixture(t *testing.T, view *View, root sourcecatalog.Root, rules *Rules, deleted []string) *Projection {
	t.Helper()
	navigation, err := view.catalog.OpenNavigation(t.Context(), "project", root)
	testutil.FailErr(t, "pin review base", err)
	t.Cleanup(func() { testutil.FailErr(t, "release review base", navigation.Close()) })
	overlay, err := view.catalog.NewTreeOverlay(t.Context(), "project", root)
	testutil.FailErr(t, "create sparse review storage", err)
	t.Cleanup(func() {
		defer overlay.Close()
		testutil.FailErr(t, "release sparse review rows", overlay.Release(context.Background()))
	})
	nodes := []sourcecatalog.OverlayNode{{Path: ".", Parent: ".", Directory: true}}
	for _, rel := range deleted {
		parts := strings.Split(rel, "/")
		parent := "."
		for i, part := range parts {
			next := path.Join(parent, part)
			nodes = append(nodes, sourcecatalog.OverlayNode{Path: next, Parent: parent, Depth: i + 1, Directory: i < len(parts)-1})
			parent = next
		}
	}
	for start := 0; start < len(nodes); start += 200 {
		testutil.FailErr(t, "record review paths", overlay.Add(t.Context(), nodes[start:min(start+200, len(nodes))]))
	}
	projection := &Projection{Root: root.ID, Navigation: navigation, Rules: rules}
	revision, err := projection.revision(t.Context())
	testutil.FailErr(t, "read review dependencies", err)
	testutil.FailErr(t, "prepare sparse additions", prepareReview(t.Context(), reviewSource{root: root.ID, rules: rules, revision: revision, open: func(ctx context.Context) (*sourcecatalog.Navigation, error) {
		return view.catalog.OpenNavigation(ctx, "project", root)
	}}, overlay))
	projection.Review = NewReviewProjection(overlay)
	return projection
}

func TestReviewProjectionMergesMissingPathsAndPreservesAncestors(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create physical directory", os.Mkdir(filepath.Join(root.Path, "present"), 0700))
	testutil.FailErr(t, "create physical file", os.WriteFile(filepath.Join(root.Path, "present", "keep.txt"), []byte("source"), 0600))
	testutil.FailErr(t, "create root file", os.WriteFile(filepath.Join(root.Path, "z.txt"), []byte("source"), 0600))
	for _, dir := range []string{".", "present"} {
		_, err := view.catalog.ObserveDirectory(t.Context(), "project", root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe physical directory", err)
	}
	rules := &Rules{}
	rules.Set(Address{Root: root.ID, Path: "."}, Disclosure{Open: true, Recursive: true})
	projection := reviewFixture(t, view, root, rules, []string{"present/keep.txt", "present/deleted.txt", "ghost/nested/gone.txt", "gone.txt", "z.txt"})
	rows, total, err := projection.Frame(t.Context(), 0, 200)
	testutil.FailErr(t, "read combined tree", err)
	expected := []string{".", "present", "present/keep.txt", "present/deleted.txt", "z.txt", "ghost", "ghost/nested", "ghost/nested/gone.txt", "gone.txt"}
	if int64(len(expected)) != total || len(rows) != len(expected) {
		t.Fatalf("rows=%+v total=%d", rows, total)
	}
	for i, rel := range expected {
		if rows[i].Address.Path != rel {
			t.Fatalf("row %d=%s want %s", i, rows[i].Address.Path, rel)
		}
		location, err := projection.Locate(t.Context(), rel)
		testutil.FailErr(t, "locate combined row", err)
		if !location.Visible || location.Index != int64(i) {
			t.Fatalf("location=%+v want row %d", location, i)
		}
	}
	if rows[2].Deleted || !rows[3].Deleted || !rows[5].Deleted || !rows[7].Deleted {
		t.Fatalf("deleted flags=%+v", rows)
	}
	ancestors, err := projection.Ancestors(t.Context(), rows[7])
	testutil.FailErr(t, "read virtual ancestors", err)
	if len(ancestors) != 3 || !ancestors[1].Deleted || ancestors[1].Index != 5 || ancestors[1].End != 8 {
		t.Fatalf("ancestors=%+v", ancestors)
	}
}

func TestReviewProjectionReplacesEmptyMarkerAndHonorsVirtualCollapse(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create empty directory", os.Mkdir(filepath.Join(root.Path, "empty"), 0700))
	for _, dir := range []string{".", "empty"} {
		_, err := view.catalog.ObserveDirectory(t.Context(), "project", root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe empty directory", err)
	}
	rules := &Rules{}
	rules.Set(Address{Root: root.ID, Path: "."}, Disclosure{Open: true, Recursive: true})
	rules.Set(Address{Root: root.ID, Path: "ghost"}, Disclosure{Open: false})
	projection := reviewFixture(t, view, root, rules, []string{"empty/deleted.txt", "ghost/deleted.txt"})
	rows, total, err := projection.Frame(t.Context(), 0, 200)
	testutil.FailErr(t, "read empty and collapsed review branches", err)
	if total != 4 || len(rows) != 4 || rows[2].Address.Path != "empty/deleted.txt" || rows[3].Address.Path != "ghost" || rows[3].Expanded {
		t.Fatalf("rows=%+v total=%d", rows, total)
	}
	for _, row := range rows {
		if row.Kind == "empty" {
			t.Fatal("empty marker survived a visible deleted file")
		}
	}
	location, err := projection.Locate(t.Context(), "ghost/deleted.txt")
	testutil.FailErr(t, "locate collapsed virtual child", err)
	if location.Visible || location.Address.Path != "ghost" || location.Index != 3 {
		t.Fatalf("location=%+v", location)
	}
}
