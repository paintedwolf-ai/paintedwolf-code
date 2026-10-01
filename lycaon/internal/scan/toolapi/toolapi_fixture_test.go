package toolapi_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// newTestCoordinator uses published source snapshots.
func newTestCoordinator(t *testing.T, store *scanbase.SQLStore, head scanbase.HeadSHAReader) *scanbase.CoordinatorImpl {
	t.Helper()
	dir := t.TempDir()
	snapshots := sourcesnapshot.New(
		store.DB(),
		sourceblob.New(filepath.Join(dir, "content")),
		filepath.Join(dir, "observations.db"),
		backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
			backgroundwork.ResourceMetadata: {Total: 1},
			backgroundwork.ResourceIO:       {Total: 1},
			backgroundwork.ResourceCPU:      {Total: 1},
		}),
	)
	t.Cleanup(func() { _ = snapshots.Close() })
	return scanbase.NewCoordinator(store, head, snapshots)
}

func claimScan(t *testing.T, store *scanbase.SQLStore, id string) *api.CodeScan {
	t.Helper()
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed.ID != id {
		t.Fatalf("claimed scan %q want %q", claimed.ID, id)
	}
	return claimed
}
