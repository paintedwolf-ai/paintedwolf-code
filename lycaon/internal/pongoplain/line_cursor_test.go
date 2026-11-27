package pongoplain

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLineCursorMatchesOffsets(t *testing.T) {
	source := "ab\ncd\n\nefg"
	cursor := newLineCursor(source)
	for _, tc := range []struct{ offset, line, col int }{
		{-1, 1, 1}, {0, 1, 1}, {2, 1, 3}, {3, 2, 1}, {5, 2, 3}, {6, 3, 1}, {7, 4, 1}, {9, 4, 3}, {99, 4, 4}, {4, 2, 2},
	} {
		if line, col := cursor.at(tc.offset); line != tc.line || col != tc.col {
			t.Errorf("at(%d) = %d:%d, want %d:%d", tc.offset, line, col, tc.line, tc.col)
		}
	}
}

func TestInspectReportsBranchPositions(t *testing.T) {
	analysis, err := Inspect("x\n  {% if a %}\n{% else %}{% endif %}", Isolated)
	testutil.FailErr(t, "inspect", err)
	if len(analysis.Branches) != 2 || analysis.Branches[0].Line != 2 || analysis.Branches[0].Col != 6 ||
		analysis.Branches[1].Line != 3 || analysis.Branches[1].Col != 4 {
		t.Fatalf("branches = %+v", analysis.Branches)
	}
}
