package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An open view keeps its roots resident: other roots opening past the
// catalog's store bound must not retire the store the view reads.
func TestOpenViewSurvivesCatalogStoreChurn(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create fixture file", os.WriteFile(filepath.Join(root.Path, "kept.txt"), []byte("source"), 0o600))
	<-view.Prepare()
	_, before, err := view.Revision(t.Context())
	testutil.FailErr(t, "read prepared view", err)

	for i := range 48 {
		other := sourcecatalog.Root{ID: fmt.Sprintf("other-%d", i), Path: t.TempDir()}
		testutil.FailErr(t, "open another root", view.catalog.Directories.WarmNavigation(t.Context(), fmt.Sprintf("project-%d", i), other))
	}

	_, after, err := view.Revision(t.Context())
	testutil.FailErr(t, "read view after catalog churn", err)
	if after != before {
		t.Fatalf("extent after churn = %+v, want %+v", after, before)
	}
}
