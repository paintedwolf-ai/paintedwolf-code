package sourcecomparison

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestComparisonProjectionIndexesDistantRowsAndExplicitFolds(t *testing.T) {
	before := strings.Repeat("context\n", 10000)
	after := strings.Repeat("context\n", 5000) + "changed\n" + strings.Repeat("context\n", 4999)
	document, err := New(api.SourceComparisonSide{Content: before}, api.SourceComparisonSide{Content: after}, nil)
	testutil.FailErr(t, "prepare comparison", err)
	full, err := document.Project(t.Context(), "full", nil, nil)
	testutil.FailErr(t, "prepare full projection", err)
	rank, anchor, err := full.Locate(t.Context(), 9999)
	testutil.FailErr(t, "locate distant source row", err)
	rows, total, err := full.Frame(t.Context(), rank, 1)
	testutil.FailErr(t, "read distant frame", err)
	if total != 10001 || len(rows) != 1 || rows[0].Source.Index != anchor.Row {
		t.Fatalf("distant frame total=%d rows=%+v", total, rows)
	}
	folded, err := document.Project(t.Context(), "changes", nil, nil)
	testutil.FailErr(t, "prepare folded projection", err)
	rank, anchor, err = folded.Locate(t.Context(), 9000)
	testutil.FailErr(t, "locate inside folded context", err)
	rows, total, err = folded.Frame(t.Context(), rank, 1)
	testutil.FailErr(t, "read folded frame", err)
	if total > 20 || len(rows) != 1 || rows[0].Source.Kind != "gap" || anchor.Row >= 9000 {
		t.Fatalf("folded frame total=%d rows=%+v", total, rows)
	}
	opened, err := document.Project(t.Context(), "changes", []Fold{{Start: 8990, End: 9010}}, nil)
	testutil.FailErr(t, "open explicit source range", err)
	rank, _, err = opened.Locate(t.Context(), 9000)
	testutil.FailErr(t, "locate revealed range", err)
	rows, _, err = opened.Frame(t.Context(), rank, 1)
	testutil.FailErr(t, "read revealed range", err)
	if len(rows) != 1 || rows[0].Source.Kind == "gap" || rows[0].Source.Index != 9000 {
		t.Fatalf("revealed row: %+v", rows)
	}
}

type countedProjectionPages struct {
	pagedview.PageStore[displaySpan]
	reads int
}

func (pages *countedProjectionPages) Read(ctx context.Context, id uint64) (pagedview.RangePage[displaySpan], error) {
	pages.reads++
	return pages.PageStore.Read(ctx, id)
}

func TestComparisonFrameSelectsOncePerDisplayRun(t *testing.T) {
	text := strings.Repeat("context\n", 10000)
	document, err := New(api.SourceComparisonSide{Content: text}, api.SourceComparisonSide{Content: text}, nil)
	testutil.FailErr(t, "prepare unchanged source", err)
	var reserved int64
	projection, err := document.Project(t.Context(), "full", nil, func(bytes int64) error { reserved = bytes; return nil })
	testutil.FailErr(t, "reserve projection", err)
	pages := &countedProjectionPages{PageStore: projection.index.Store}
	projection.index.Store = pages
	rows, _, err := projection.Frame(t.Context(), 9000, 200)
	testutil.FailErr(t, "read contiguous distant rows", err)
	if len(rows) != 200 || rows[199].Source.Index != 9199 || pages.reads > 2 {
		t.Fatalf("frame rows=%d page reads=%d", len(rows), pages.reads)
	}
	if projection.RetainedBytes() > reserved || reserved > 128<<10 {
		t.Fatalf("compact plan reserved=%d retained=%d", reserved, projection.RetainedBytes())
	}
	_, err = document.Project(t.Context(), "full", nil, func(int64) error { return pagedview.ErrBudget })
	if !errors.Is(err, pagedview.ErrBudget) {
		t.Fatalf("reservation rejection=%v", err)
	}
}

func TestComparisonProjectionSplitPairsDoNotOccupyDuplicateRanks(t *testing.T) {
	document, err := New(api.SourceComparisonSide{Content: strings.Repeat("old\n", 3000)}, api.SourceComparisonSide{Content: strings.Repeat("new\n", 3000)}, nil)
	testutil.FailErr(t, "prepare paired comparison", err)
	projection, err := document.Project(t.Context(), "split", nil, nil)
	testutil.FailErr(t, "prepare split projection", err)
	rank, anchor, err := projection.Locate(t.Context(), 5500)
	testutil.FailErr(t, "locate right-hand row", err)
	rows, total, err := projection.Frame(t.Context(), rank, 1)
	testutil.FailErr(t, "read paired frame", err)
	if total != 3000 || rank != 2500 || anchor.Row != 2500 || len(rows) != 1 || rows[0].Source.Peer == nil || rows[0].Source.Peer.Index != 5500 {
		t.Fatalf("paired frame rank=%d total=%d rows=%+v", rank, total, rows)
	}
}

func TestDisplayExtentMatchesTheProjectionItMeasures(t *testing.T) {
	before := strings.Repeat("context\n", 400) + "old\n" + strings.Repeat("context\n", 400)
	after := "new first\n" + strings.Repeat("context\n", 400) + "new\n" + strings.Repeat("context\n", 400)
	document, err := New(api.SourceComparisonSide{Content: before}, api.SourceComparisonSide{Content: after}, nil)
	testutil.FailErr(t, "prepare comparison", err)
	for _, mode := range []string{"full", "changes", "split", "before", "after"} {
		projection, err := document.Project(t.Context(), mode, nil, nil)
		testutil.FailErr(t, "project "+mode, err)
		total, err := projection.Extent(t.Context())
		testutil.FailErr(t, "extent "+mode, err)
		extent, err := document.DisplayExtent(t.Context(), mode)
		testutil.FailErr(t, "measure "+mode, err)
		if int64(extent.Rows) != total {
			t.Fatalf("%s: measured %d rows, projection holds %d", mode, extent.Rows, total)
		}
	}
	changes, err := document.DisplayExtent(t.Context(), "changes")
	testutil.FailErr(t, "measure changes", err)
	// Two change areas leave unchanged runs folded around them.
	if changes.Folds == 0 || changes.Rows >= 800 {
		t.Fatalf("changes extent %+v folds nothing", changes)
	}
	if _, err := document.DisplayExtent(t.Context(), "sideways"); !errors.Is(err, ErrMode) {
		t.Fatalf("unknown mode err = %v", err)
	}
}
