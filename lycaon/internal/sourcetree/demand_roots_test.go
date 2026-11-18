package sourcetree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSelectingOutsideRootsKeepsProjectDisclosures(t *testing.T) {
	view, project := viewFixture(t)
	closed := Address{Root: project.ID, Path: "."}
	testutil.FailErr(t, "collapse the project root", view.Toggle(t.Context(), nil, closed, false))
	outside := Root{Root: sourcecatalog.Root{ID: "outside", Path: t.TempDir(), OnDemand: true}, Label: "Outside"}
	testutil.FailErr(t, "create outside file", os.WriteFile(filepath.Join(outside.Path, "readme.txt"), []byte("hello"), 0o600))
	changed, err := view.ReplaceOnDemandRoots(t.Context(), []Root{outside})
	testutil.FailErr(t, "add outside location", err)
	if !changed {
		t.Fatal("the outside root did not change")
	}
	frame, err := frameForTest(t, view, t.Context(), FrameRequest{Limit: 10})
	testutil.FailErr(t, "read the project and outside roots", err)
	if len(frame.Rows) != 2 || frame.Rows[0].Expanded || frame.Rows[1].Expanded {
		t.Fatalf("the project root should stay collapsed and the outside root should start closed: %+v", frame.Rows)
	}
	location := Address{Root: outside.ID, Path: "."}
	err = view.Disclose(t.Context(), nil, IntentEntry{Address: location, Disclosure: Disclosure{Open: true, Recursive: true}})
	if !errors.Is(err, ErrAddress) {
		t.Fatalf("recursive outside expansion error = %v, want %v", err, ErrAddress)
	}
	testutil.FailErr(t, "expand the outside root", view.Toggle(t.Context(), nil, location, false))
	testutil.WaitFor(t, "the selected outside folder settles", func() bool {
		frame, err = frameForTest(t, view, t.Context(), FrameRequest{Limit: 10})
		return err == nil && len(frame.Rows) == 3
	})
	if frame.Rows[2].Address != (Address{Root: outside.ID, Path: "readme.txt"}) {
		t.Fatalf("the outside file was not listed: %+v", frame.Rows)
	}
	_, err = view.ReplaceOnDemandRoots(t.Context(), nil)
	testutil.FailErr(t, "remove the outside location", err)
	if intent := view.Intent(); len(intent) != 1 || intent[0].Address != closed || intent[0].Disclosure.Open {
		t.Fatalf("project disclosure changed: %+v", intent)
	}
}
