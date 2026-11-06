package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramObservationCommitsWithoutSourceBindings(t *testing.T) {
	type observationStore interface {
		Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
		CommitProgramObservation(context.Context, string, string, *api.CommandProgramReceipt, string) (string, error)
		LoadLedger(context.Context, string) (evidence.Ledger, error)
	}
	for _, memory := range []bool{false, true} {
		name := "sql"
		if memory {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			var owner observationStore
			if memory {
				owner = NewMemory()
			} else {
				dir := t.TempDir()
				db := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
				testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, dir)
				owner = NewSQL(db)
			}
			sess, err := owner.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			receipt := &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation", State: "running"}
			if handle, err := owner.CommitProgramObservation(t.Context(), sess.ID, "command", nil, "unbound"); err != nil || handle != "" {
				t.Fatalf("unbound outcome acquired handle: %q %v", handle, err)
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if handle, err := owner.CommitProgramObservation(cancelled, sess.ID, "command", receipt, "cancelled"); err == nil || handle != "" {
				t.Fatalf("cancelled observation committed: %q %v", handle, err)
			}
			if handle, err := owner.CommitProgramObservation(t.Context(), "missing", "command", receipt, "wrong session"); err == nil || handle != "" {
				t.Fatalf("missing session acquired evidence: %q %v", handle, err)
			}
			first, err := owner.CommitProgramObservation(t.Context(), sess.ID, "command", receipt, "src/unseen.go:42: invented source")
			testutil.FailErr(t, "commit running observation", err)
			receipt.State = "completed"
			second, err := owner.CommitProgramObservation(t.Context(), sess.ID, "command_output", receipt, "test failed")
			testutil.FailErr(t, "commit settled observation", err)
			if first != "command#1" || second != "command#2" {
				t.Fatalf("observation handles = %q, %q", first, second)
			}
			ledger, err := owner.LoadLedger(t.Context(), sess.ID)
			testutil.FailErr(t, "load observations", err)
			if len(ledger.Handles) != 2 || len(ledger.ByPath) != 0 || ledger.Handles[second].SourceTool != "command_output" || ledger.Handles[first].SourceDelivery != nil {
				t.Fatalf("observations changed source authority: %+v", ledger)
			}
		})
	}
}
