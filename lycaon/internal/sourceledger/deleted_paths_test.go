package sourceledger

import (
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestDeletedPathsScopePaginationAndCurrentOccupant(t *testing.T) {
	store, ctx := openLedger(t)
	checkout := sourcebranch.ForWorktree("checkout")
	record := func(branch sourcebranch.ID, path string, op api.SourceChangeOp, origin api.SourceChangeOrigin) {
		t.Helper()
		input := RecordInput{ProjectID: "p1", RootID: "r1", Path: path, BranchID: branch, Op: op, Origin: origin, SessionID: "chat"}
		if op == api.SourceChangeOpDelete {
			input.Before = []byte("old\n")
		} else {
			input.After = []byte("new\n")
		}
		mustRecord(t, store, ctx, input)
	}
	record(sourcebranch.Trunk, "deleted.go", api.SourceChangeOpDelete, api.SourceChangeOriginAgent)
	record(sourcebranch.Trunk, "replaced.go", api.SourceChangeOpDelete, api.SourceChangeOriginAgent)
	record(sourcebranch.Trunk, "replaced.go", api.SourceChangeOpCreate, api.SourceChangeOriginUser)
	record(sourcebranch.Trunk, "user.go", api.SourceChangeOpDelete, api.SourceChangeOriginUser)
	record(checkout, "checkout.go", api.SourceChangeOpDelete, api.SourceChangeOriginAgent)
	for _, withoutUser := range []bool{false, true} {
		baseline := Baseline{Kind: BaselineSession, SessionID: "chat", WithoutUserEdits: withoutUser, RootBranches: map[string]sourcebranch.ID{"r1": sourcebranch.Trunk}}
		before := int64(0)
		found := make(map[string]bool)
		for {
			page, err := store.DeletedPaths(ctx, "p1", baseline, 1, before)
			testutil.FailErr(t, "read deleted paths", err)
			for _, path := range page.Paths {
				found[path.Path] = true
			}
			if page.NextBeforeOrdinal == 0 {
				break
			}
			if before != 0 && page.NextBeforeOrdinal >= before {
				t.Fatal("page did not advance")
			}
			before = page.NextBeforeOrdinal
		}
		if !found["deleted.go"] || found["checkout.go"] || found["replaced.go"] || found["user.go"] == withoutUser {
			t.Fatalf("without user=%v paths=%v", withoutUser, found)
		}
	}
	page, err := store.DeletedPaths(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "chat", RootBranches: map[string]sourcebranch.ID{"r1": checkout}}, 200, 0)
	testutil.FailErr(t, "read checkout deletions", err)
	if len(page.Paths) != 1 || page.Paths[0].Path != "checkout.go" {
		t.Fatalf("checkout paths=%+v", page)
	}
}
