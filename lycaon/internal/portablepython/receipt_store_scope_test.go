package portablepython_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func TestFileReceiptStoreAmbiguousLegacyIDsFailClosedAndPropagateErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := portablepython.NewFileReceiptStore(dir)
	if err != nil {
		t.Fatalf("NewFileReceiptStore: %v", err)
	}

	// Seed two distinct scopes that share a legacy duplicate execution ID ("py-exec-1").
	if err := store.PersistExecution(ctx, pythonevidence.ExecutionRecord{
		ExecutionID: "py-exec-1",
		CallID:      "call-a",
		SessionID:   "sess-a",
		Actor:       "implement",
		View:        "project",
	}); err != nil {
		t.Fatalf("persist exec A: %v", err)
	}
	if err := store.PersistExecution(ctx, pythonevidence.ExecutionRecord{
		ExecutionID: "py-exec-1",
		CallID:      "call-b",
		SessionID:   "sess-b",
		Actor:       "implement",
		View:        "project",
	}); err != nil {
		t.Fatalf("persist exec B: %v", err)
	}

	scopeA := pythonevidence.ViewerScope{Role: pythonevidence.RoleCoordinator, SessionID: "sess-a", Actor: "implement", View: "project"}
	scopeB := pythonevidence.ViewerScope{Role: pythonevidence.RoleCoordinator, SessionID: "sess-b", Actor: "implement", View: "project"}

	for _, sc := range []pythonevidence.ViewerScope{scopeA, scopeB} {
		if _, err := store.ProjectPersistedForDen("py-exec-1", sc); !errors.Is(err, portablepython.ErrAmbiguousExecutionID) {
			t.Fatalf("ProjectPersistedForDen(%+v) err = %v, want ErrAmbiguousExecutionID", sc, err)
		}
		if _, err := store.LoadAdmissionsForScope("py-exec-1", sc); !errors.Is(err, portablepython.ErrAmbiguousExecutionID) {
			t.Fatalf("LoadAdmissionsForScope(%+v) err = %v, want ErrAmbiguousExecutionID", sc, err)
		}
	}

	// Seed a clean unique execution, then corrupt admissions.jsonl and verify
	// errors are propagated rather than returning an incomplete projection.
	if err := store.PersistExecution(ctx, pythonevidence.ExecutionRecord{
		ExecutionID:     "py-exec-unique",
		CallID:          "call-u",
		SessionID:       "sess-a",
		Actor:           "implement",
		View:            "project",
		CleanCompletion: true,
	}); err != nil {
		t.Fatalf("persist unique exec: %v", err)
	}
	if err := store.PersistAdmission(ctx, pythonevidence.LedgerAdmission{
		ParentOperationID: "call-u",
		Record: pythonadmission.AdmissionRecord{
			ExecutionID: "py-exec-unique",
			OperationID: "op-1",
			Actor:       "implement",
			View:        "project",
			Family:      pythonruntime.EffectFilesystemRead,
			Operation:   "open",
			Decision:    pythonruntime.DecisionAllow,
		},
	}); err != nil {
		t.Fatalf("persist valid admission: %v", err)
	}

	// Unauthorized scope is denied before reading admissions.
	if _, err := store.LoadAdmissionsForScope("py-exec-unique", scopeB); !errors.Is(err, pythonevidence.ErrAccessDenied) {
		t.Fatalf("unauthorized LoadAdmissionsForScope err = %v, want ErrAccessDenied", err)
	}

	// Corrupt admissions.jsonl -> both ProjectPersistedForDen and LoadAdmissionsForScope must error.
	f, err := os.OpenFile(filepath.Join(dir, "admissions.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open admissions.jsonl: %v", err)
	}
	_, _ = f.WriteString("{corrupt\n")
	_ = f.Close()

	if _, err := store.ProjectPersistedForDen("py-exec-unique", scopeA); err == nil {
		t.Fatal("expected ProjectPersistedForDen to propagate corrupted admissions.jsonl error")
	}
	if _, err := store.LoadAdmissionsForScope("py-exec-unique", scopeA); err == nil {
		t.Fatal("expected LoadAdmissionsForScope to propagate corrupted admissions.jsonl error")
	}
}
