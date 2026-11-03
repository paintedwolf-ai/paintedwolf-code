package sourceview

import (
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/testutil"
	"runtime"
	"strings"
	"testing"
)

// TestDiffScriptComparesTransformedLinesAndBoundsItsStorage covers the shared
// engine both diff presentations use.
func TestDiffScriptComparesTransformedLinesAndBoundsItsStorage(t *testing.T) {
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
	// Unchanged edges do not enter the edit frontier, but the output script still owns capacity.
	if _, err := DiffScript([]string{"x", "1", "y"}, []string{"x", "2", "y"}, nil, 81); err != nil {
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

func TestDiffScriptScatteredChangesInFiftyThousandLinesStayBounded(t *testing.T) {
	oldLines := make([]string, 50000)
	newLines := make([]string, len(oldLines))
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("line %05d\n", i)
		newLines[i] = oldLines[i]
	}
	for i := 0; i < 100; i++ {
		newLines[i*500+10] = fmt.Sprintf("changed %d\n", i)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ops, err := DiffScript(oldLines, newLines, nil, 8<<20)
	runtime.ReadMemStats(&after)
	testutil.FailErr(t, "compare scattered edits", err)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 12<<20 {
		t.Fatalf("comparison allocated %d bytes", allocated)
	}
	var old, new []string
	changes := 0
	for _, op := range ops {
		switch op.Tag {
		case ' ':
			if oldLines[op.Old] != newLines[op.New] {
				t.Fatal("unchanged operation changed a line")
			}
			old = append(old, oldLines[op.Old])
			new = append(new, newLines[op.New])
		case '-':
			old = append(old, oldLines[op.Old])
			changes++
		case '+':
			new = append(new, newLines[op.New])
			changes++
		default:
			t.Fatalf("unknown operation %q", op.Tag)
		}
	}
	if strings.Join(old, "") != strings.Join(oldLines, "") || strings.Join(new, "") != strings.Join(newLines, "") || changes != 200 {
		t.Fatalf("script lost input or edit count: old=%d new=%d changes=%d", len(old), len(new), changes)
	}
	if hunks := DiffHunks(ops, 3, nil); len(hunks) != 100 {
		t.Fatalf("hunks=%d", len(hunks))
	}
}
