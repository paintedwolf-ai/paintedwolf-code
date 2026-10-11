package sourcetree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func viewFixture(t *testing.T) (*View, sourcecatalog.Root) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := sourcecatalog.Root{ID: "root", Path: t.TempDir()}
	catalog := sourcecatalog.New()
	view := New(t.Context(), pagedview.Scope{Person: "person", Project: "project", Workspace: "workspace"}, []Root{{Root: root, Label: "Source"}}, catalog, nil)
	t.Cleanup(func() { view.Close(); testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	return view, root
}

func TestRecursiveDisclosureSupportsDistantLocate(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 40 {
		dir := filepath.Join(root.Path, fmt.Sprintf("dir-%03d", i))
		testutil.FailErr(t, "create fixture directory", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "create fixture file", os.WriteFile(filepath.Join(dir, "file.txt"), []byte("source"), 0600))
	}
	observeFixture(t, view, root)
	location, _, err := locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "dir-039/file.txt"})
	testutil.FailErr(t, "locate distant file", err)
	if !location.Visible || location.Index != 80 {
		t.Fatalf("location=%+v", location)
	}
	frame, err := frameForTest(t, view, t.Context(), FrameRequest{Offset: location.Index, Limit: 10})
	testutil.FailErr(t, "read distant frame", err)
	if len(frame.Rows) != 1 || frame.Rows[0].Address.Path != "dir-039/file.txt" || frame.Extent.Rows != 81 {
		t.Fatalf("frame=%+v", frame)
	}
	if len(frame.Ancestors) != 2 || frame.Ancestors[1].Index != 79 {
		t.Fatalf("ancestors=%+v", frame.Ancestors)
	}
	anchor := Address{Root: root.ID, Path: "dir-039/file.txt"}
	window, err := frameForTest(t, view, t.Context(), FrameRequest{Anchor: &anchor, Before: 20, Limit: 100})
	testutil.FailErr(t, "read viewport before end anchor", err)
	if window.Span.Start != 60 || window.Span.End != 81 || window.ResolvedAnchor != anchor || window.Rows[len(window.Rows)-1].Address != anchor {
		t.Fatalf("end viewport=%+v", window)
	}
	testutil.FailErr(t, "collapse root", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: false, Recursive: true}}))
	location, _, err = locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "dir-039/file.txt"})
	testutil.FailErr(t, "locate hidden descendant", err)
	if location.Visible || location.Index != 0 || location.Address.Path != "." {
		t.Fatalf("hidden location=%+v", location)
	}
}

func TestFindVisibleLabelsDoesNotExpandFolders(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 12 {
		dir := filepath.Join(root.Path, fmt.Sprintf("match-%02d", i))
		testutil.FailErr(t, "create search fixture", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "create hidden descendant", os.WriteFile(filepath.Join(dir, "match-child.txt"), []byte("source"), 0600))
	}
	_, err := view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	revision, _, err := view.Revision(t.Context())
	testutil.FailErr(t, "read search basis", err)
	offset, count := int64(0), 0
	for {
		page, err := findForTest(t, view, t.Context(), revision.Projection, "match", offset, 5, false)
		testutil.FailErr(t, "find visible labels", err)
		count += len(page.Matches)
		if page.Revision != revision || len(page.Matches) > 5 {
			t.Fatalf("incoherent search page=%+v", page)
		}
		if page.Complete {
			break
		}
		if page.Next <= offset {
			t.Fatal("search did not advance")
		}
		offset = page.Next
	}
	if count != 12 {
		t.Fatalf("visible matches=%d", count)
	}
	location, after, err := locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "match-00/match-child.txt"})
	testutil.FailErr(t, "read unchanged disclosure", err)
	if location.Visible || after != revision {
		t.Fatal("search changed folder disclosure")
	}
}

func TestRecursiveDisclosureOpensFoldersDiscoveredLater(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create original directory", os.Mkdir(filepath.Join(root.Path, "original"), 0700))
	testutil.FailErr(t, "create original file", os.WriteFile(filepath.Join(root.Path, "original", "file.txt"), []byte("source"), 0600))
	observeFixture(t, view, root)
	testutil.FailErr(t, "create later directory", os.Mkdir(filepath.Join(root.Path, "later"), 0700))
	testutil.FailErr(t, "create later file", os.WriteFile(filepath.Join(root.Path, "later", "file.txt"), []byte("source"), 0600))
	view.catalog.InvalidateRoot(root.Path, "later")
	_, err := view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh root", err)
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, "later", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe later directory for another consumer", err)
	location, _, err := locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "later/file.txt"})
	testutil.FailErr(t, "locate later child", err)
	if !location.Visible {
		t.Fatalf("recursive disclosure hid observed child: %+v", location)
	}
	original, _, err := locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "original/file.txt"})
	testutil.FailErr(t, "locate previously expanded child", err)
	if !original.Visible {
		t.Fatal("completion collapsed a published folder")
	}
}

func TestRequestedFrameRefreshesAfterRepositoryChange(t *testing.T) {
	view, root := viewFixture(t)
	<-view.Prepare()
	_, err := frameForTest(t, view, t.Context(), FrameRequest{Limit: 20})
	testutil.FailErr(t, "register requested frame", err)
	testutil.FailErr(t, "create file after frame", os.WriteFile(filepath.Join(root.Path, "new.txt"), []byte("source"), 0600))
	view.catalog.InvalidateRoot(root.Path, "new.txt")
	view.repositoryChanged(t.Context(), repochange.Event{ProjectDir: root.Path, Kind: repochange.WorktreeChanged, Paths: []string{"new.txt"}})
	testutil.WaitFor(t, 10*time.Second, func() bool {
		location, _, err := locateForTest(t, view, t.Context(), Address{Root: root.ID, Path: "new.txt"})
		return err == nil && location.Visible
	})

}

func observeFixture(t *testing.T, view *View, root sourcecatalog.Root) {
	t.Helper()
	testutil.FailErr(t, "set recursive disclosure", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: true, Recursive: true}}))
	err := filepath.WalkDir(root.Path, func(name string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root.Path, name)
		if err != nil {
			return err
		}
		_, err = view.catalog.Directories.ObserveDirectory(t.Context(), view.scope.Project, root, filepath.ToSlash(relative), sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		return err
	})
	testutil.FailErr(t, "observe fixture", err)
	<-view.Prepare()
}

func TestDisclosureBatchPreservesRootAndRejectsPartialChanges(t *testing.T) {
	view, root := viewFixture(t)
	address := Address{Root: root.ID, Path: "."}
	testutil.FailErr(t, "expand root", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	testutil.FailErr(t, "collapse descendants", view.Disclose(t.Context(), nil,
		IntentEntry{Address: address, Disclosure: Disclosure{Recursive: true}},
		IntentEntry{Address: address, Disclosure: Disclosure{Open: true}},
	))
	view.mu.Lock()
	rootOpen := view.rules.At(address).Open
	childOpen := view.rules.At(Address{Root: root.ID, Path: "child"}).Open
	revision := view.revision
	view.mu.Unlock()
	if !rootOpen || childOpen {
		t.Fatalf("disclosure state: root=%v child=%v", rootOpen, childOpen)
	}
	err := view.Disclose(t.Context(), &RulesBasis{Replace: true},
		IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}},
		IntentEntry{Address: Address{Root: "missing", Path: "."}, Disclosure: Disclosure{Open: true}},
	)
	if !errors.Is(err, ErrUnknownRoot) {
		t.Fatalf("invalid batch error: %v", err)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.revision != revision || view.rules.At(Address{Root: root.ID, Path: "child"}).Open {
		t.Fatal("rejected batch changed disclosure intent")
	}
}

func TestToggleUsesAcceptedDisclosureBeforeRowsAreRead(t *testing.T) {
	view, root := viewFixture(t)
	address := Address{Root: root.ID, Path: "child"}
	testutil.FailErr(t, "create child", os.Mkdir(filepath.Join(root.Path, "child"), 0700))
	for range 2 {
		testutil.FailErr(t, "toggle folder", view.Toggle(t.Context(), nil, address, false))
	}
	view.mu.Lock()
	open := view.rules.At(address).Open
	view.mu.Unlock()
	if open {
		t.Fatal("two toggles left the folder open")
	}
	testutil.FailErr(t, "expand subtree", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	testutil.FailErr(t, "collapse descendants", view.Toggle(t.Context(), nil, address, true))
	view.mu.Lock()
	parent := view.rules.At(address).Open
	child := view.rules.At(Address{Root: root.ID, Path: "child/nested"}).Open
	view.mu.Unlock()
	if !parent || child {
		t.Fatalf("descendant collapse: parent=%v child=%v", parent, child)
	}
	if err := view.Toggle(t.Context(), nil, Address{Root: root.ID, Path: "/"}, false); !errors.Is(err, ErrAddress) {
		t.Fatalf("invalid address error=%v", err)
	}
}

func TestDisplayedBasisToggleSupersedesRecursiveIntentOnce(t *testing.T) {
	view, root := viewFixture(t)
	rootAddress := Address{Root: root.ID, Path: "."}
	first := Address{Root: root.ID, Path: "first"}
	second := Address{Root: root.ID, Path: "second"}
	testutil.FailErr(t, "start recursive expansion", view.Disclose(t.Context(), nil, IntentEntry{
		Address: rootAddress, Disclosure: Disclosure{Open: true, Recursive: true},
	}))

	basis := &RulesBasis{Replace: true}
	testutil.FailErr(t, "replace recursive intent", view.Toggle(t.Context(), basis, first, false))
	basis.Replace = false
	testutil.FailErr(t, "compose second toggle", view.Toggle(t.Context(), basis, second, false))

	view.mu.Lock()
	defer view.mu.Unlock()
	if current := view.rules.At(rootAddress); current.Recursive {
		t.Fatalf("recursive intent survived displayed-basis navigation: %+v", current)
	}
	if !view.rules.At(first).Open || !view.rules.At(second).Open {
		t.Fatalf("displayed-basis toggles did not compose: first=%+v second=%+v", view.rules.At(first), view.rules.At(second))
	}
}

// A recursive disclosure opens the tree and leaves collapsed trees closed, to be
// opened on request one level at a time. A committed dependency tree opens with
// the rest; an ignored one collapses.
func TestRecursiveDisclosureKeepsCollapsedTreesClosed(t *testing.T) {
	view, root := viewFixture(t)
	for _, rel := range []string{"src/deep/keep.txt", "build/out.txt", "build/nested/x.txt", "vendor/dep.txt", "installed/dep.txt", ".gitignore"} {
		testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(filepath.Join(root.Path, rel)), 0700))
		testutil.FailErr(t, "create fixture file", os.WriteFile(filepath.Join(root.Path, rel), []byte("installed/\n"), 0600))
	}
	rows := func() []string {
		t.Helper()
		frame, err := frameForTest(t, view, t.Context(), FrameRequest{Offset: 0, Limit: 50})
		testutil.FailErr(t, "read frame", err)
		out := make([]string, 0, len(frame.Rows))
		for _, row := range frame.Rows {
			label := row.Address.Path
			if row.Kind == "directory" && row.Expanded {
				label += "/"
			}
			out = append(out, label)
		}
		return out
	}
	expandAll := func(dir string) {
		t.Helper()
		testutil.FailErr(t, "expand all", view.Disclose(t.Context(), nil,
			IntentEntry{Address: Address{Root: root.ID, Path: dir}, Disclosure: Disclosure{Open: true, Recursive: true}}))
	}
	derived := func() []string {
		view.mu.Lock()
		defer view.mu.Unlock()
		out := []string{}
		for _, address := range view.rules.Derived() {
			out = append(out, address.Path)
		}
		return out
	}
	// Boundaries are derived from published structure, never from a blocked
	// command. A timeout reports what the catalog itself answers.
	awaitBoundaries := func(want []string) {
		t.Helper()
		if testutil.WaitForNoFatal(10*time.Second, func() bool { return slices.Equal(derived(), want) }) {
			return
		}
		boundaries, ready, err := view.catalog.Directories.CollapseBoundaries(t.Context(), view.scope.Project, root, ".")
		t.Fatalf("view boundaries = %v, want %v; catalog answers %v (ready %v, err %v)", derived(), want, boundaries, ready, err)
	}

	expandAll(".")
	awaitBoundaries([]string{"build", "installed"})
	if intent := view.Intent(); len(intent) != 1 || intent[0].Address.Path != "." {
		t.Fatalf("host-closed boundaries leaked into intent: %+v", intent)
	}
	want := []string{".", "build", "installed", "src", "src/deep", "src/deep/keep.txt", "vendor", "vendor/dep.txt", ".gitignore"}
	if got := rows(); !slices.Equal(got, markExpanded(got, want)) {
		t.Fatalf("expand all rows = %v, want generated trees closed and committed vendoring open (%v)", got, want)
	}

	testutil.FailErr(t, "open a boundary", view.Toggle(t.Context(), nil, Address{Root: root.ID, Path: "build"}, false))
	got := rows()
	if !slices.Contains(got, "build/nested") || !slices.Contains(got, "build/out.txt") || slices.Contains(got, "build/nested/x.txt") {
		t.Fatalf("opening a boundary did not show exactly one level: %v", got)
	}

	expandAll("build")
	got = rows()
	if !slices.Contains(got, "build/nested/x.txt") {
		t.Fatalf("expand all inside a collapsed tree did not open it: %v", got)
	}

	expandAll(".")
	awaitBoundaries([]string{"build", "installed"})
	if got := rows(); slices.Contains(got, "build/out.txt") {
		t.Fatalf("expanding all again left a collapsed tree open: %v", got)
	}
}

// Boundaries converge even when the catalog finishes publishing before the
// recursive rule is installed. Each round starts cold to race the first pass.
func TestRecursiveDisclosureConvergesWhateverTheCatalogTiming(t *testing.T) {
	for round := range 8 {
		view, root := viewFixture(t)
		for _, rel := range []string{"src/keep.txt", "build/out.txt"} {
			testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(filepath.Join(root.Path, rel)), 0700))
			testutil.FailErr(t, "create fixture file", os.WriteFile(filepath.Join(root.Path, rel), []byte("source"), 0600))
		}
		testutil.FailErr(t, "expand all", view.Disclose(t.Context(), nil,
			IntentEntry{Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: true, Recursive: true}}))
		converged := testutil.WaitForNoFatal(10*time.Second, func() bool {
			view.mu.Lock()
			defer view.mu.Unlock()
			derived := view.rules.Derived()
			return len(derived) == 1 && derived[0].Path == "build"
		})
		if !converged {
			t.Fatalf("round %d: the view never derived its boundary", round)
		}
	}
}

// markExpanded restores the trailing slash the row reader adds to open folders,
// so a want list can be written as plain paths.
func markExpanded(got, want []string) []string {
	open := make(map[string]bool, len(got))
	for _, row := range got {
		open[strings.TrimSuffix(row, "/")] = strings.HasSuffix(row, "/")
	}
	out := make([]string, 0, len(want))
	for _, path := range want {
		if open[path] {
			path += "/"
		}
		out = append(out, path)
	}
	return out
}
