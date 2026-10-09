package projectsource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeTrashInterruptionDoesNotInventRecoveryReceipt(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed selected source", os.WriteFile(filepath.Join(root, "selected"), []byte("retained in Trash"), 0600))
	trashPath := filepath.Join(t.TempDir(), "selected")
	id := uuid.NewString()
	interrupted := false
	service.Effects.SetTrashMover(func(_ context.Context, path string) (desktoptrash.Receipt, error) {
		_, err := testTrashRelocate(path, trashPath)
		testutil.FailErr(t, "native relocation", err)
		panic("process stopped before receipt acknowledgement")
	})
	func() {
		defer func() { interrupted = recover() != nil }()
		_ = service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "selected"})
	}()
	if !interrupted {
		t.Fatal("interruption fixture did not reach native publication")
	}
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "recover interrupted operation", restarted.Recover(t.Context()))
	row, found, err := restarted.Journal.load(t.Context(), id)
	testutil.FailErr(t, "read interrupted operation", err)
	if !found || row.Status != sourceMutationFailed {
		t.Fatalf("unacknowledged receipt became successful: %+v", row)
	}
	history, err := restarted.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read recovery history", err)
	if history.Undo != nil {
		t.Fatalf("invented native recovery: %+v", history.Undo)
	}
	if data, err := os.ReadFile(trashPath); err != nil || string(data) != "retained in Trash" {
		t.Fatalf("native item changed during recovery: %q %v", data, err)
	}
}
