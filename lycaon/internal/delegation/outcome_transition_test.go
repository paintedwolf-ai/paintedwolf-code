package delegation

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCanceledWorkerSettlesLegAndDelegationAsCanceled(t *testing.T) {
	store := NewMemoryStore()
	d, err := store.Create(t.Context(), api.Delegation{ProjectID: "project", Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{
		{ID: "leg", Title: "Fixture leg", Status: api.LegStatusRunning, WorkerID: "worker"},
	})
	testutil.FailErr(t, "create delegation", err)
	manager := &Manager{Store: store}
	testutil.FailErr(t, "record cancellation", manager.RecordOutcome(t.Context(), d.ID, "leg", "worker", api.WorkerResult{Status: "canceled"}))
	got, err := store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read delegation", err)
	if got.Status != api.DelegationStatusCanceled || got.Legs[0].Status != api.LegStatusCanceled || got.Legs[0].CompletedAt == nil {
		t.Fatalf("canceled delegation: %+v legs=%+v", got, got.Legs)
	}
}

func TestAbortedLegRetainsCancellationAfterOutcomeDelivery(t *testing.T) {
	store := NewMemoryStore()
	d, err := store.Create(t.Context(), api.Delegation{ProjectID: "project", Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{
		{ID: "leg", Title: "Fixture leg", Status: api.LegStatusRunning, WorkerID: "worker"},
	})
	testutil.FailErr(t, "create delegation", err)
	settled := time.Now().UTC()
	testutil.FailErr(t, "abort delegation", store.Abort(t.Context(), d.ID, settled, "stop"))
	manager := &Manager{Store: store}
	for _, status := range []string{"canceled", "complete", "failed"} {
		testutil.FailErr(t, "deliver late outcome", manager.RecordOutcome(t.Context(), d.ID, "leg", "worker", api.WorkerResult{Status: status}))
	}
	got, err := store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read delegation", err)
	if got.Status != api.DelegationStatusAborted || got.Legs[0].Status != api.LegStatusCanceled || !got.Legs[0].CompletedAt.Equal(settled) {
		t.Fatalf("late delivery changed settled state: %+v legs=%+v", got, got.Legs)
	}
}

func TestLegOutcomeRequiresCurrentWorkerAndOpenOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current api.LegStatus
		worker  string
		want    bool
	}{
		{"running owner", api.LegStatusRunning, "current", true},
		{"dispatched owner", api.LegStatusDispatched, "current", true},
		{"held owner", api.LegStatusHeld, "current", true},
		{"prior worker", api.LegStatusRunning, "prior", false},
		{"awaiting resume", api.LegStatusRetryPending, "current", false},
		{"complete", api.LegStatusComplete, "current", false},
		{"failed", api.LegStatusFailed, "current", false},
		{"canceled", api.LegStatusCanceled, "current", false},
		{"not dispatched", api.LegStatusPending, "current", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStore()
			d, err := store.Create(t.Context(), api.Delegation{ProjectID: "project", Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{
				{ID: "leg", Title: "Original title", Status: tc.current, WorkerID: "current"},
			})
			testutil.FailErr(t, "create delegation", err)
			settled := time.Now().UTC()
			changed, err := store.RecordLegOutcome(t.Context(), api.Leg{
				ID: "leg", DelegationID: d.ID, WorkerID: tc.worker, Status: api.LegStatusComplete,
				Title: "Ignored metadata", CompletedAt: &settled, Result: &api.WorkerResult{Status: "complete"},
			})
			testutil.FailErr(t, "record leg outcome", err)
			if changed != tc.want {
				t.Fatalf("changed=%v want=%v", changed, tc.want)
			}
			got, err := store.GetLeg(t.Context(), d.ID, "leg")
			testutil.FailErr(t, "read leg", err)
			wantStatus := tc.current
			if tc.want {
				wantStatus = api.LegStatusComplete
			}
			if got.Status != wantStatus || got.WorkerID != "current" || got.Title != "Original title" {
				t.Fatalf("outcome changed ownership or metadata: %+v", got)
			}
		})
	}
}
