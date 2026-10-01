//go:build stress

package sourcecomparison

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestStressCurrentMillionLinesRetainsConstantSnapshotMemory(t *testing.T) {
	memory, disk := pagedview.NewBudget(4<<20), pagedview.NewBudget(64<<20)
	document, err := NewCurrent(t.Context(), strings.NewReader(strings.Repeat("line\n", 1_000_000)), textfile.UTF8, "million.txt", memory, disk)
	testutil.FailErr(t, "capture million line snapshot", err)
	defer document.Close()
	row, err := document.RowAtLine(t.Context(), 999999)
	testutil.FailErr(t, "locate distant million line coordinate", err)
	frame, total, err := document.Frame(t.Context(), int64(row), 2)
	testutil.FailErr(t, "read distant million line frame", err)
	if total != 1_000_000 || len(frame) != 2 || frame[0].Source.AfterLine != 999999 || memory.Used() != currentMemoryBytes {
		t.Fatalf("unbounded snapshot: rows=%d frame=%+v memory=%d", total, frame, memory.Used())
	}
}
