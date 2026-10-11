package projectsource

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourcePreparationOwnsRecoveryUntilReceiptPublication(t *testing.T) {
	service, p, _, _ := sourceMutationFixture(t)
	ledger := service.settlement.recorder.(*sourceledger.Store)
	interrupted := errors.New("injected preparation interruption")
	_, err := service.execute(t.Context(), "operation", p.ID, "digest", func() (*sourceMutationPlan, error) {
		if err := ledger.Retention.MaintainBlobs(t.Context()); !errors.Is(err, sourceledger.ErrBlobMaintenanceDeferred) {
			t.Fatalf("collector entered uncommitted preparation: %v", err)
		}
		return nil, interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("preparation error=%v", err)
	}
	testutil.FailErr(t, "collect after preparation exits", ledger.Retention.MaintainBlobs(t.Context()))
}
