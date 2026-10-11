package workeroutcomes

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFindingDeliveryRetriesRetainsAndPagesWithoutLoss(t *testing.T) {
	store := findings.NewMemoryStore()
	ctx := workercontext.WithJob(t.Context(), "reader")
	manager := NewNotes(nil, store, nil, nil)
	for i := 0; i < 23; i++ {
		_, err := store.Append(ctx, "child", "peer", fmt.Sprintf("contract %d: %s", i, strings.Repeat("界", 250)), "module.go", "full body")
		testutil.FailErr(t, "append peer finding", err)
	}
	seen := map[int64]bool{}
	for round := 0; len(seen) < 23 && round < 30; round++ {
		page, err := manager.PrepareSiblingNotes(ctx, "child", 8)
		testutil.FailErr(t, "prepare findings", err)
		retryPage, err := manager.PrepareSiblingNotes(ctx, "child", 8)
		testutil.FailErr(t, "retry findings", err)
		if !reflect.DeepEqual(page, retryPage) {
			t.Fatal("unacknowledged request changed delivery")
		}
		if round == 0 && !page.More {
			t.Fatal("overflow was not disclosed")
		}
		size := 0
		for _, n := range page.Notes {
			seen[n.ID] = true
			size += len(n.Summary) + len(n.Ref) + len(n.Agent) + 128
		}
		if size > 4096 || len(page.Notes) > 8 {
			t.Fatalf("unbounded peer context: %d bytes, %d notes", size, len(page.Notes))
		}
		testutil.FailErr(t, "commit successful response", manager.CommitSiblingNotes(ctx, "child", fmt.Sprint(round), page.Cursor, page.Notes))
		manager = NewNotes(nil, store, nil, nil)
	}
	if len(seen) != 23 {
		t.Fatalf("lost findings: saw %d of 23", len(seen))
	}
	first, err := manager.PrepareSiblingNotes(ctx, "child", 8)
	testutil.FailErr(t, "read retained context", err)
	next, err := manager.PrepareSiblingNotes(ctx, "child", 8)
	testutil.FailErr(t, "repeat retained context", err)
	if len(first.Notes) == 0 || !reflect.DeepEqual(first, next) {
		t.Fatal("retained context changed with no new findings")
	}
}
