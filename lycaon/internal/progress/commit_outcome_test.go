package progress

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	api "github.com/lycaon/lycaon/pkg/api"
)

type failingTranscript struct {
	inner   TranscriptTransactions
	failure error
	cancel  context.CancelFunc
}

func (f failingTranscript) AppendMessagesWith(ctx context.Context, id string, mutate func(*sql.Tx) ([]api.Message, error)) error {
	return f.inner.AppendMessagesWith(ctx, id, func(tx *sql.Tx) ([]api.Message, error) {
		msgs, err := mutate(tx)
		if err != nil {
			return nil, err
		}
		if f.cancel != nil {
			f.cancel()
			return nil, ctx.Err()
		}
		return msgs, f.failure
	})
}

type trackingReconciler struct {
	txCalls  int
	releases int
}

func (r *trackingReconciler) Reconciliation(string) *Reconciliation {
	return &Reconciliation{
		Jobs:        []string{"job-1"},
		Unaccounted: []UnaccountedResult{{Handle: "scout#1", Worker: "scout"}},
	}
}

func (r *trackingReconciler) UnaccountedResults(string) []UnaccountedResult {
	return []UnaccountedResult{{Handle: "scout#1", Worker: "scout"}}
}

func (r *trackingReconciler) AcknowledgeTx(_ context.Context, _ *sql.Tx, _, _ string, _ []string) (AckOutcome, error) {
	r.txCalls++
	return AckOutcome{AccountedJobIDs: []string{"job-1"}}, nil
}

func (r *trackingReconciler) Release(string, []string) {
	r.releases++
}

func TestApplyPublishesOnlySettledStoreOutcomes(t *testing.T) {
	for _, mode := range []string{"store failure", "callback cancellation"} {
		t.Run(mode, func(t *testing.T) {
			store := newProgressFixture(t, "unsettled")
			_, err := store.Apply(t.Context(), "unsettled", Mutation{
				Replace: []RowSpec{{Label: "Deliver", State: ProgressStatePending}},
			}, ApplyOptions{})
			testutil.FailErr(t, "seed preserved work", err)
			before, err := store.Snapshot(t.Context(), "unsettled")
			testutil.FailErr(t, "observe current progress", err)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sentinel := errors.New("commit unavailable")
			tx := failingTranscript{inner: store.transcript, failure: sentinel}
			if mode == "callback cancellation" {
				tx.cancel = cancel
				sentinel = context.Canceled
			}
			failingStore := NewSQLStore(store.database, tx)

			publications := 0
			release := RegisterWriteObserver(func(_ context.Context, event WriteEvent) {
				if event.SessionID == "unsettled" {
					publications++
				}
			})
			defer release()

			reconciler := &trackingReconciler{}
			done := ProgressStateDone
			result, err := failingStore.Apply(ctx, "unsettled", Mutation{
				Update:             []UpdateRowSpec{{ID: "p1", State: &done}},
				AcknowledgeResults: []string{"scout#1"},
			}, ApplyOptions{Reconciler: reconciler})
			if !errors.Is(err, sentinel) {
				t.Fatalf("unsettled result error: %v", err)
			}
			if result.Updated || result.StateChanged || len(result.ClosedItemIDs) != 0 || result.ReconciliationStatus != "not_requested" {
				t.Fatalf("uncommitted proposal escaped as outcome: %+v", result)
			}
			if publications != 0 || reconciler.releases != 0 {
				t.Fatalf("failed commit published events=%d releases=%d", publications, reconciler.releases)
			}
			after, err := store.Snapshot(t.Context(), "unsettled")
			testutil.FailErr(t, "read preserved snapshot", err)
			if !before.Equal(after) {
				t.Fatal("unsettled publication changed stored progress")
			}
		})
	}
}
