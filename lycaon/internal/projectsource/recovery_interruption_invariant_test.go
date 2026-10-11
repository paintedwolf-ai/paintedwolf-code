package projectsource

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryRetentionSurvivesEveryDeletionSettlementBoundary(t *testing.T) {
	for _, boundary := range []string{"prepared", "effect applied", "receipt applied"} {
		t.Run(boundary, func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			ledger := service.settlement.recorder.(*sourceledger.Store)
			verify, reviewed := seedRetainedEntry(t, root, "binary")
			removal := agentRemoval(project, "entry", reviewed)
			removal.Record.OperationID = uuid.NewString()
			interrupted := errors.New("interrupted before deletion")
			ctx := WithSourceEffect(t.Context(), func() error { panic(interrupted) })
			func() {
				defer func() {
					got, ok := recover().(error)
					if !ok || !errors.Is(got, interrupted) {
						t.Fatalf("effect boundary interruption=%v", got)
					}
				}()
				_, _ = service.RemoveEntry(ctx, removal)
			}()
			row, found, err := service.Journal.load(t.Context(), removal.Record.OperationID)
			testutil.FailErr(t, "load interrupted deletion", err)
			if !found || row.Status != sourceMutationPrepared || row.Plan.RecoveryCount == 0 {
				t.Fatalf("deletion lacks durable recovery: %+v", row)
			}
			testutil.FailErr(t, "collect prepared recovery", ledger.Retention.SweepBlobs(t.Context()))
			if boundary != "prepared" {
				testutil.FailErr(t, "apply deletion without settlement", service.Effects.applyMutation(t.Context(), row))
				if boundary == "receipt applied" {
					row.Status = sourceMutationFileApplied
				}
				testutil.FailErr(t, "persist interrupted receipt", service.Journal.update(t.Context(), row))
			}
			service = recoverWithMaintenance(t, service, ledger)
			service = recoverWithMaintenance(t, service, ledger)
			if boundary == "prepared" {
				verify()
				_, err = service.RemoveEntry(t.Context(), removal)
				testutil.FailErr(t, "explicitly retry unexecuted deletion", err)
			}
			service = recoverWithMaintenance(t, service, ledger)
			row, found, err = service.Journal.load(t.Context(), removal.Record.OperationID)
			testutil.FailErr(t, "load settled deletion", err)
			if !found || row.Status != sourceMutationCommitted {
				t.Fatalf("settled deletion=%+v", row)
			}
			var count int
			testutil.FailErr(t, "count recovered history", service.Journal.db.QueryRowContext(t.Context(),
				`SELECT count(*) FROM source_history_entries WHERE project_id=?`, project.ID).Scan(&count))
			if count != 1 {
				t.Fatalf("repeated recovery created %d history entries", count)
			}
			undoHistoryHead(t, service, project, removal.Record.OperationID)
			testutil.FailErr(t, "collect after recovered Undo", ledger.Retention.SweepBlobs(t.Context()))
			verify()
		})
	}
}
