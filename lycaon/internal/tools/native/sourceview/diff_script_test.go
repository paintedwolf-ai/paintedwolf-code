package sourceview

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestDiffScriptComparesTransformedLinesAndBoundsItsTable covers the shared
// engine both diff presentations use.
func TestDiffScriptComparesTransformedLinesAndBoundsItsTable(t *testing.T) {
	oldLines, newLines := []string{"a", "b", "c", "d"}, []string{"a", "B", "c", "d", "e"}
	ops, err := DiffScript(oldLines, newLines, nil, 0)
	testutil.FailErr(t, "compute bounded diff script", err)
	var tags strings.Builder
	for _, op := range ops {
		tags.WriteByte(op.Tag)
	}
	if tags.String() != " -+  +" {
		t.Fatalf("script = %q %+v", tags.String(), ops)
	}
	fold := strings.EqualFold
	folded, err := DiffScript(oldLines, newLines, fold, 0)
	if err != nil || len(folded) != 5 || folded[1].Tag != ' ' || folded[1].Old != 1 || folded[1].New != 1 {
		t.Fatalf("folded = %+v %v", folded, err)
	}
	// Common prefix and suffix never enter the table.
	if _, err := DiffScript([]string{"x", "1", "y"}, []string{"x", "2", "y"}, nil, 4); err != nil {
		t.Fatalf("trimmed comparison refused: %v", err)
	}
	if _, err := DiffScript([]string{"1", "2", "3"}, []string{"4", "5", "6"}, nil, 15); !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("table ceiling = %v", err)
	}
	hunks := DiffHunks(ops, 0, func(i int) bool { return ops[i].Tag != ' ' && i != 5 })
	if len(hunks) != 1 || hunks[0].OldStart != 2 || hunks[0].OldCount != 1 || hunks[0].NewCount != 1 {
		t.Fatalf("hunks without the ignored addition = %+v", hunks)
	}
	far := DiffHunks(ops, 1, nil)
	if len(far) != 1 || far[0].StartOp != 0 || far[0].EndOp != 6 {
		t.Fatalf("merged hunks = %+v", far)
	}
}
