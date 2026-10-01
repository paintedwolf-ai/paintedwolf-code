package delegation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func delegationStores(t *testing.T, check func(*testing.T, Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { check(t, NewMemoryStore()) })
	t.Run("SQL", func(t *testing.T) {
		database := testdbfixture.Open(t, "delegations.db")
		testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
		check(t, NewSQLStore(database))
	})
}

func TestSettlementRequiresTerminalLegsAndHasOneWinner(t *testing.T) {
	delegationStores(t, func(t *testing.T, store Store) {
		d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{{ID: "leg", Title: "Fixture leg", Status: api.LegStatusRunning, WorkerID: "worker"}})
		testutil.FailErr(t, "create delegation", err)
		changed, err := store.Settle(t.Context(), d.ID)
		testutil.FailErr(t, "try unsettled leg", err)
		if changed {
			t.Fatal("settled a running leg")
		}
		leg := d.Legs[0]
		leg.Status = api.LegStatusComplete
		now := time.Now().UTC()
		leg.CompletedAt = &now
		changed, err = store.RecordLegOutcome(t.Context(), leg)
		testutil.FailErr(t, "record completion", err)
		if !changed {
			t.Fatal("completion did not settle running leg")
		}
		for _, want := range []bool{true, false} {
			changed, err = store.Settle(t.Context(), d.ID)
			testutil.FailErr(t, "settle delegation", err)
			if changed != want {
				t.Fatalf("settlement winner=%v want=%v", changed, want)
			}
		}
		got, err := store.Get(t.Context(), d.ID)
		testutil.FailErr(t, "read delegation", err)
		if got.Status != api.DelegationStatusDone || got.Phase != api.DelegationPhaseDone {
			t.Fatalf("settled delegation: %+v", got)
		}
	})
}

func TestTerminalDelegationPreservesItsGraph(t *testing.T) {
	for _, tc := range []struct {
		status    api.DelegationStatus
		legStatus api.LegStatus
	}{
		{api.DelegationStatusDone, api.LegStatusComplete},
		{api.DelegationStatusFailed, api.LegStatusFailed},
		{api.DelegationStatusCanceled, api.LegStatusCanceled},
		{api.DelegationStatusAborted, api.LegStatusComplete},
	} {
		status, legStatus := tc.status, tc.legStatus
		t.Run(string(status), func(t *testing.T) {
			delegationStores(t, func(t *testing.T, store Store) {
				d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{{ID: "leg", Title: "Fixture leg", Status: legStatus, WorkerID: "worker"}})
				testutil.FailErr(t, "create delegation", err)
				if status == api.DelegationStatusAborted {
					testutil.FailErr(t, "abort delegation", store.Abort(t.Context(), d.ID, time.Now().UTC(), "original reason"))
				} else {
					_, err = store.Settle(t.Context(), d.ID)
					testutil.FailErr(t, "settle delegation", err)
				}
				testutil.FailErr(t, "repeat abort", store.Abort(t.Context(), d.ID, time.Now().UTC(), "late reason"))
				changed, err := store.Settle(t.Context(), d.ID)
				testutil.FailErr(t, "repeat settlement", err)
				if changed {
					t.Fatal("terminal delegation settled twice")
				}
				if err := store.AddLeg(t.Context(), d.ID, api.Leg{ID: "late-leg", Title: "Late fixture leg", Status: api.LegStatusPending}); !errors.Is(err, ErrDelegationSettled) {
					t.Fatalf("add leg to settled graph: %v", err)
				}
				stale := d.Legs[0]
				stale.Status = api.LegStatusPending
				testutil.FailErr(t, "apply stale leg metadata", store.UpdateLeg(t.Context(), stale))
				got, err := store.Get(t.Context(), d.ID)
				testutil.FailErr(t, "read delegation", err)
				if got.Status != status || got.Phase != api.DelegationPhaseDone || len(got.Legs) != 1 || got.Legs[0].Status != legStatus {
					t.Fatalf("late mutation changed graph: %+v legs=%+v", got, got.Legs)
				}
				if status == api.DelegationStatusAborted && got.Reason != "original reason" {
					t.Fatalf("abort reason changed: %q", got.Reason)
				}
			})
		})
	}
}

type beforeSettleStore struct {
	Store
	before func()
}

func (s beforeSettleStore) Settle(ctx context.Context, id string) (bool, error) {
	s.before()
	return s.Store.Settle(ctx, id)
}

func TestAbortWinningCloseoutDoesNotEmitCloseoutCallback(t *testing.T) {
	delegationStores(t, func(t *testing.T, store Store) {
		d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "Complete fixture work", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{{ID: "leg", Title: "Fixture leg", Status: api.LegStatusComplete}})
		testutil.FailErr(t, "create delegation", err)
		callbacks := 0
		manager := &Manager{Store: beforeSettleStore{Store: store, before: func() {
			testutil.FailErr(t, "abort before settle commit", store.Abort(t.Context(), d.ID, time.Now().UTC(), "stop"))
		}},
			OnCloseout: func(context.Context, string, string, string) { callbacks++ }}
		testutil.FailErr(t, "retry closeout", manager.RetryCloseout(t.Context(), d.ID))
		if callbacks != 0 {
			t.Fatalf("closeout callbacks after abort: %d", callbacks)
		}
	})
}
