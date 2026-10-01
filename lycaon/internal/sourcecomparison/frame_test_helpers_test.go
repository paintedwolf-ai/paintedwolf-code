package sourcecomparison

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type comparisonTestFrame struct {
	Rows     []api.SourceReaderRow
	End      int
	Complete bool
}

func frameForTest(t *testing.T, d *Document, offset, limit int, mode string) comparisonTestFrame {
	t.Helper()
	projection, err := d.Project(t.Context(), mode, nil, nil)
	testutil.FailErr(t, "project comparison", err)
	rows, total, err := projection.Frame(t.Context(), int64(offset), limit)
	testutil.FailErr(t, "read comparison frame", err)
	frame := comparisonTestFrame{End: offset + len(rows), Complete: int64(offset+len(rows)) >= total}
	for _, row := range rows {
		frame.Rows = append(frame.Rows, row.Source)
	}
	return frame
}
func findForTest(t *testing.T, d *Document, query string, cursor SearchCursor, limit int, sensitive bool) SearchPage {
	t.Helper()
	page, err := d.Find(t.Context(), query, cursor, limit, sensitive)
	testutil.FailErr(t, "find comparison text", err)
	return page
}
