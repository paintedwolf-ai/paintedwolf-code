package sourcecatalog

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPresentationPagerSharesBoundsAndDiscardsReleasedState(t *testing.T) {
	c := New()
	first, releaseFirst, err := c.Directories.acquirePresentation(t.Context())
	testutil.FailErr(t, "open presentation pager", err)
	defer releaseFirst()
	second, releaseSecond, err := c.Directories.acquirePresentation(t.Context())
	testutil.FailErr(t, "join presentation pager", err)
	defer releaseSecond()
	if first != second {
		t.Fatal("presenters allocated separate pagers")
	}
	_, err = first.ExecContext(t.Context(), `INSERT INTO projection_meta(id) VALUES('retained')`)
	testutil.FailErr(t, "record retained presentation", err)
	releaseFirst()
	var count int
	testutil.FailErr(t, "read independently retained data", second.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM projection_meta`).Scan(&count))
	if count != 1 {
		t.Fatalf("retained rows=%d", count)
	}
	releaseSecond()
	third, releaseThird, err := c.Directories.acquirePresentation(t.Context())
	testutil.FailErr(t, "open fresh presentation pager", err)
	defer releaseThird()
	testutil.FailErr(t, "verify no orphaned data", third.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM projection_meta`).Scan(&count))
	if count != 0 {
		t.Fatalf("orphaned rows=%d", count)
	}
	_, err = third.ExecContext(t.Context(), `PRAGMA max_page_count=64`)
	testutil.FailErr(t, "lower scratch capacity", err)
	_, err = third.ExecContext(t.Context(), `INSERT INTO projection_rows VALUES('large',0,'file','.',0,0,1,zeroblob(1048576))`)
	if !errors.Is(err, pagedview.ErrBudget) && !pagedview.StorageFull(err) {
		t.Fatalf("disk ceiling error=%v", err)
	}
}

func TestProjectionRowsPropagatesPagerStorageFull(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(t.Context())) })
	projection, err := catalog.Directories.NewProjectionRows(t.Context(), "project", Root{ID: "root", Path: t.TempDir()})
	testutil.FailErr(t, "create projection", err)
	defer projection.Close()
	_, err = projection.db.ExecContext(t.Context(), `PRAGMA max_page_count=64`)
	testutil.FailErr(t, "lower projection capacity", err)
	rows := []ProjectionRecord{
		{Rank: 0, Address: "a", Parent: ".", End: 1, Body: make([]byte, pagedview.MaxFrameBytes)},
		{Rank: 1, Address: "b", Parent: ".", End: 2, Body: make([]byte, pagedview.MaxFrameBytes)},
	}
	err = projection.Write(t.Context(), rows, nil)
	if !pagedview.StorageFull(err) {
		t.Fatalf("projection storage ceiling error=%v", err)
	}
}
