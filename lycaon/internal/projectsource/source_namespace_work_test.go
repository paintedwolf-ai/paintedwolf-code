package projectsource

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTrackedDirectoryMoveWorkDoesNotScaleWithDescendants(t *testing.T) {
	var reference []int64
	for _, size := range []int{1, 128} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			testutil.FailErr(t, "create tracked tree", os.Mkdir(filepath.Join(root, "tree"), 0700))
			ledger := service.settlement.recorder.(*sourceledger.Store)
			records := make([]sourceledger.RecordInput, size)
			for i := range records {
				rel := fmt.Sprintf("tree/%d", i)
				testutil.FailErr(t, "create descendant", os.WriteFile(filepath.Join(root, rel), nil, 0600))
				records[i] = sourceledger.RecordInput{RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: rel}, ProjectID: p.ID, Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate, After: []byte{}}
			}
			testutil.FailErr(t, "track descendants", ledger.RecordBatch(t.Context(), records))
			original, err := ledger.History.ResolveHead(t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "tree/0")
			testutil.FailErr(t, "resolve original descendant", err)
			count := func() int64 {
				var value int64
				testutil.FailErr(t, "read durable write counter", service.Journal.db.QueryRowContext(t.Context(), `SELECT generation FROM history_storage_clock WHERE id=1`).Scan(&value))
				return value
			}
			id := uuid.NewString()
			var work lifecycleWork
			ctx := WithSourceProgress(t.Context(), work.observe)
			var writes []int64
			for _, action := range []string{"move", "undo", "redo"} {
				before := count()
				switch action {
				case "move":
					_, err = service.Rename(ctx, id, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "tree", To: "moved"})
				case "undo":
					_, err = service.Undo(ctx, uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
				case "redo":
					_, err = service.Redo(ctx, uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
				}
				testutil.FailErr(t, action, err)
				writes = append(writes, count()-before)
			}
			if work.verificationBytes() != 0 {
				t.Fatal("native directory move read descendant content")
			}
			moved, err := ledger.History.ResolveHead(t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "moved/0")
			testutil.FailErr(t, "resolve moved descendant", err)
			if moved.FileID != original.FileID || moved.VersionID != original.VersionID {
				t.Fatalf("move rewrote descendant history: %+v", moved)
			}
			if reference == nil {
				reference = writes
			} else {
				for i, got := range writes {
					if got != reference[i] {
						t.Fatalf("%d descendants: transition %d wrote %d rows, one descendant wrote %d", size, i, got, reference[i])
					}
				}
			}
		})
	}
}
