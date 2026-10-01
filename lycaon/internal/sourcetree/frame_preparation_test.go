package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompleteExpansionAddressesEveryDestinationWithoutFilesystemReads(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 120 {
		directory := filepath.Join(root.Path, fmt.Sprintf("dir-%03d", i))
		testutil.FailErr(t, "create directory", os.Mkdir(directory, 0700))
		for j := range 20 {
			testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(directory, fmt.Sprintf("file-%02d.txt", j)), []byte("source"), 0600))
		}
	}
	address := Address{Root: root.ID, Path: "."}
	testutil.FailErr(t, "expand", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	presentation := captureForTest(t, view)
	_, extent := presentation.Revision()
	if extent.Rows != 2521 || !extent.Complete {
		t.Fatalf("extent=%+v", extent)
	}
	// Removing the checkout cannot affect retained structure reads.
	testutil.FailErr(t, "remove filesystem rows", os.RemoveAll(filepath.Join(root.Path, "dir-110")))
	for _, offset := range []int64{2400, 0, 1250, 2000, 400, 2300, 2500, 1200} {
		frame, err := presentation.Frame(t.Context(), FrameRequest{Offset: offset, Limit: 100})
		testutil.FailErr(t, "read arbitrary destination", err)
		if frame.Extent != extent || len(frame.Rows) != int(min(100, extent.Rows-offset)) {
			t.Fatalf("frame=%+v", frame)
		}
		for _, row := range frame.Rows {
			if row.Kind == "loading" {
				t.Fatalf("loading row=%+v", row)
			}
		}
	}
	location, _, err := presentation.Locate(t.Context(), Address{Root: root.ID, Path: "dir-110/file-19.txt"})
	testutil.FailErr(t, "locate removed file", err)
	if !location.Visible || location.Index != 2331 {
		t.Fatalf("location=%+v", location)
	}
}

func TestCollapsedExceptionsDoNotRequireDescendantDiscovery(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create hidden descendant", os.MkdirAll(filepath.Join(root.Path, "closed", "nested"), 0700))
	testutil.FailErr(t, "create visible descendant", os.MkdirAll(filepath.Join(root.Path, "open", "nested"), 0700))
	testutil.FailErr(t, "set mixed intent", view.Disclose(t.Context(), nil,
		IntentEntry{Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: true, Recursive: true}},
		IntentEntry{Address: Address{Root: root.ID, Path: "closed"}, Disclosure: Disclosure{Open: false}}))
	presentation := captureForTest(t, view)
	frame, err := presentation.Frame(t.Context(), FrameRequest{Limit: 20})
	testutil.FailErr(t, "read mixed presentation", err)
	if !frame.Extent.Complete {
		t.Fatal("collapsed exception prevented publication")
	}
	for _, row := range frame.Rows {
		if row.Address.Path == "closed/nested" || row.Kind == "loading" {
			t.Fatalf("unexpected row=%+v", row)
		}
	}
}
