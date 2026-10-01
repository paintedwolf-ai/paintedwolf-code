//go:build stress

package findings_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStressFindingBurstPagesWithoutDroppingOlderNotes(t *testing.T) {
	store := newFindingsStore(t, "root")
	for i := 0; i < 1000; i++ {
		appendFinding(t, store, "root", fmt.Sprintf("worker-%d", i%32), fmt.Sprintf("interface %d", i), "shared.go")
	}
	cursor := int64(0)
	seen := make(map[int64]bool)
	for {
		rows, next, err := store.Recent(t.Context(), "root", "", cursor, 8, time.Time{})
		testutil.FailErr(t, "page burst", err)
		if len(rows) == 0 {
			break
		}
		if next <= cursor || len(rows) > 8 {
			t.Fatalf("invalid page: rows=%d cursor=%d next=%d", len(rows), cursor, next)
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatalf("repeated finding %d", row.ID)
			}
			seen[row.ID] = true
		}
		cursor = next
	}
	if len(seen) != 1000 {
		t.Fatalf("retained %d of 1000 findings", len(seen))
	}
}
