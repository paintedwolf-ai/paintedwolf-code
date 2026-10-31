package projectsource

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Retained copy plans from existing history account for captured AfterSize;
// native Trash receipts retain no duplicate source objects.
func TestHistoryQuotaAccountsForRetainedCopiesAndNativeReceipts(t *testing.T) {
	for _, memory := range []bool{false, true} {
		t.Run(fmt.Sprint(memory), func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			if memory {
				service = NewSourceMutationService(nil, nil)
				installTestTrash(t, service)
			}
			service.History.SetRecoveryQuotaBytes(6)
			var latest string
			for i, body := range []string{"first", "last"} {
				from, to := fmt.Sprintf("from-%d", i), fmt.Sprintf("to-%d", i)
				testutil.FailErr(t, "seed copy", os.WriteFile(filepath.Join(root, from), []byte(body), 0o600))
				sourceRoot, fromAbs, fromRel, err := locateLifecycleSource(project, project.Roots[0].ID, from)
				testutil.FailErr(t, "locate copy source", err)
				toAbs, toRel, err := resolveLifecycleDest(sourceRoot, to)
				testutil.FailErr(t, "locate copy destination", err)
				identity, err := fspath.EntryIdentity(fromAbs)
				testutil.FailErr(t, "read source identity", err)
				id := uuid.NewString()
				_, err = service.execute(t.Context(), id, project.ID, id, testSourceOperationMemory(t), func() (*sourceMutationPlan, error) {
					return &sourceMutationPlan{
						sourceMutationPublication: sourceMutationPublication{EntryIdentity: identity},
						sourceMutationAttribution: sourceMutationAttribution{ProjectID: project.ID, WorkspaceID: project.WorkspaceID()},
						Kind:                      "copy", RootID: project.Roots[0].ID, RootPath: sourceRoot.Path, Path: toRel, FromPath: fromRel, ToPath: toRel, FromAbs: fromAbs, ToAbs: toAbs, EntryKind: SourceEntryFile, Changed: true,
					}, nil
				})
				testutil.FailErr(t, "complete retained copy", err)
				latest = id
			}
			state, err := service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read latest copy", err)
			if state.Undo == nil || state.Undo.ID != latest {
				t.Fatalf("copy Undo=%+v", state)
			}
			undoHistoryHead(t, service, project, latest)
			state, err = service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read pruned copy history", err)
			if state.Undo != nil || state.Redo == nil {
				t.Fatalf("captured copy bytes were not pruned: %+v", state)
			}
			redoHistoryHead(t, service, project, latest)
			_, err = service.Copy(t.Context(), uuid.NewString(), project, SourceCopyRequest{RootID: project.Roots[0].ID, From: "from-0", To: "native-copy"})
			testutil.FailErr(t, "copy with native recovery receipt", err)
			state, err = service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read native copy", err)
			if state.Undo == nil {
				t.Fatal("native copy Undo missing")
			}
			undoHistoryHead(t, service, project, state.Undo.ID)
			state, err = service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read retained copy after native receipt", err)
			if state.Undo == nil || state.Undo.ID != latest {
				t.Fatalf("native receipt consumed source recovery quota: %+v", state)
			}
		})
	}
}
