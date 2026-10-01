package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

// The budget detects contention with the failed transaction's write lock.
const commitFailureBudget = 2 * time.Second

// Recording the failure requires releasing the failed transaction first.
func TestSourceMutationCommitFailureRecordsErrorPromptly(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(rootPath, "note.txt"), []byte("before"), 0o640))
	// Break the attribution write that shares the commit transaction.
	_, err := service.db.ExecContext(t.Context(), `DROP TABLE source_operations`)
	testutil.FailErr(t, "drop source operations", err)

	opID := uuid.NewString()
	started := time.Now()
	_, writeErr := service.Write(t.Context(), opID, p, SourceWriteRequest{
		Path: "note.txt", RootID: p.Roots[0].ID, Content: "after", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("before")),
	})
	elapsed := time.Since(started)
	if writeErr == nil {
		t.Fatal("write succeeded with no ledger table to record it")
	}
	if elapsed > commitFailureBudget {
		t.Fatalf("failed commit took %s (budget %s) — the failure note is contending with its own transaction",
			elapsed, commitFailureBudget)
	}

	row, found, err := service.load(t.Context(), opID)
	testutil.FailErr(t, "load mutation", err)
	if !found {
		t.Fatal("no source_mutations row survived the failed commit")
	}
	if row.Status == sourceMutationCommitted {
		t.Fatalf("status = %q — a commit that rolled back was recorded as committed", row.Status)
	}
	if row.Error == "" {
		t.Fatalf("error column empty for status %q — the failure note never reached the row", row.Status)
	}
}
