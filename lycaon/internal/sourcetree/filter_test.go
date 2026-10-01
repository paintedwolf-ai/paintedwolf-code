package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFilterKeepsVisibleAncestorsAndSupportsDistantFrames(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 260 {
		dir := filepath.Join(root.Path, fmt.Sprintf("dir-%03d", i))
		testutil.FailErr(t, "create filter directory", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "create matching file", os.WriteFile(filepath.Join(dir, "target.txt"), []byte("content"), 0600))
		testutil.FailErr(t, "create unrelated file", os.WriteFile(filepath.Join(dir, "other.txt"), []byte("content"), 0600))
	}
	observeFixture(t, view, root)
	filtered, err := view.Filter(t.Context(), "TARGET")
	testutil.FailErr(t, "build filtered projection", err)
	release := filtered.Retain()
	defer release()
	filtered.Close()
	revision, extent := filtered.Revision()
	if extent.Rows != 521 {
		t.Fatalf("filtered extent=%+v", extent)
	}
	location, err := filtered.Locate(t.Context(), Address{Root: root.ID, Path: "dir-259/target.txt"})
	testutil.FailErr(t, "locate distant filtered row", err)
	if location.Index != 520 || !location.Visible {
		t.Fatalf("location=%+v", location)
	}
	frame, err := filtered.Frame(t.Context(), FrameRequest{Offset: location.Index, Limit: 20})
	testutil.FailErr(t, "read distant filtered frame", err)
	if len(frame.Rows) != 1 || frame.Rows[0].Name != "target.txt" || len(frame.Ancestors) != 2 {
		t.Fatalf("frame=%+v", frame)
	}
	if frame.Ancestors[0].Index != 0 || frame.Ancestors[0].End != 521 || frame.Ancestors[1].Index != 519 || frame.Ancestors[1].End != 521 {
		t.Fatalf("filtered ancestor bounds=%+v", frame.Ancestors)
	}
	hidden, err := filtered.Locate(t.Context(), Address{Root: root.ID, Path: "dir-259/other.txt"})
	testutil.FailErr(t, "locate filtered-out row", err)
	if hidden.Visible || hidden.Index != 519 {
		t.Fatalf("hidden=%+v", hidden)
	}
	page, err := filtered.Find(t.Context(), revision.Projection, "target", 518, 10, false)
	testutil.FailErr(t, "search filtered tail", err)
	if !page.Complete || len(page.Matches) != 2 || page.Matches[1].Index != 520 {
		t.Fatalf("search=%+v", page)
	}
	testutil.FailErr(t, "collapse live tree", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: false, Recursive: true}}))
	frame, err = filtered.Frame(t.Context(), FrameRequest{Offset: 520, Limit: 1})
	testutil.FailErr(t, "read retained filter after disclosure", err)
	if len(frame.Rows) != 1 || frame.Rows[0].Name != "target.txt" {
		t.Fatalf("retained filter=%+v", frame)
	}
}

func TestFilterDoesNotDiscoverCollapsedDescendants(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create collapsed fixture", os.Mkdir(filepath.Join(root.Path, "folder"), 0700))
	testutil.FailErr(t, "create hidden matching file", os.WriteFile(filepath.Join(root.Path, "folder", "target.txt"), []byte("content"), 0600))
	<-view.Prepare()
	filtered, err := view.Filter(t.Context(), "target")
	testutil.FailErr(t, "filter current projection", err)
	defer filtered.Close()
	_, extent := filtered.Revision()
	if extent.Rows != 0 {
		t.Fatalf("filter opened a collapsed directory: %+v", extent)
	}
	outside, err := filtered.Locate(t.Context(), Address{Root: root.ID, Path: "folder/target.txt"})
	testutil.FailErr(t, "locate outside an empty selection", err)
	if outside.Visible || outside.Address.Path != "." {
		t.Fatalf("outside=%+v", outside)
	}
	self, err := view.Filter(t.Context(), "folder")
	testutil.FailErr(t, "filter collapsed directory name", err)
	defer self.Close()
	_, extent = self.Revision()
	frame, err := self.Frame(t.Context(), FrameRequest{Limit: 20})
	testutil.FailErr(t, "read matching collapsed directory", err)
	if extent.Rows != 2 || len(frame.Rows) != 2 || frame.Rows[1].Expanded {
		t.Fatalf("self match=%+v", frame)
	}
}
