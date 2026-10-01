package delegation

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowCancellationSettlesDelegationGraph(t *testing.T) {
	database := testdbfixture.Open(t, "workflow-cancellation.db")
	store := NewSQLStore(database)
	testdbseed.InsertSession(t, database, "coordinator", testdbseed.DefaultProjectID)
	finishedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var legs []api.Leg
	for _, status := range []api.LegStatus{api.LegStatusPending, api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusRetryPending, api.LegStatusHeld, api.LegStatusComplete, api.LegStatusFailed, api.LegStatusCanceled} {
		leg := api.Leg{Title: string(status), Status: status}
		if status == api.LegStatusComplete || status == api.LegStatusFailed || status == api.LegStatusCanceled {
			leg.CompletedAt = &finishedAt
		}
		legs = append(legs, leg)
	}
	created, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkflowRunID: "run", Task: "work", Strategy: api.HuntStrategyFileBased}, "coordinator", legs)
	testutil.FailErr(t, "create graph", err)
	testutil.FailErr(t, "cancel workflow", store.CancelActiveByWorkflowRunID(t.Context(), "run"))
	got, err := store.Get(t.Context(), created.ID)
	testutil.FailErr(t, "read graph", err)
	if got.Status != api.DelegationStatusCanceled || got.Phase != api.DelegationPhaseDone {
		t.Fatalf("delegation = %+v", got)
	}
	for _, leg := range got.Legs {
		switch api.LegStatus(leg.Title) {
		case api.LegStatusComplete, api.LegStatusFailed, api.LegStatusCanceled:
			if string(leg.Status) != leg.Title || leg.CompletedAt == nil || !leg.CompletedAt.Equal(finishedAt) {
				t.Fatalf("terminal leg changed: %+v", leg)
			}
		default:
			if leg.Status != api.LegStatusCanceled || leg.CompletedAt == nil {
				t.Fatalf("active leg not settled: %+v", leg)
			}
		}
	}
	testutil.FailErr(t, "repeat cancellation", store.CancelActiveByWorkflowRunID(t.Context(), "run"))
	again, err := store.Get(t.Context(), created.ID)
	testutil.FailErr(t, "read repeated cancellation", err)
	for i, leg := range again.Legs {
		if !leg.CompletedAt.Equal(*got.Legs[i].CompletedAt) {
			t.Fatalf("completion changed: %+v", leg)
		}
	}
}
