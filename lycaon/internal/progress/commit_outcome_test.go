package progress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type unsettledProgressStore struct {
	Store
	failure error
	cancel  context.CancelFunc
}

func (s unsettledProgressStore) Update(ctx context.Context, id string, change func(Snapshot) (string, error)) (Snapshot, error) {
	if s.cancel != nil {
		return s.Store.Update(ctx, id, func(current Snapshot) (string, error) {
			content, err := change(current)
			s.cancel()
			return content, err
		})
	}
	current, err := s.Store.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	content, err := change(current)
	if err != nil {
		return current, err
	}
	return snapshotVersion(current.RunID, content, "uncommitted"), s.failure
}

type unsettledReconciliation struct {
	token            string
	acknowledgements int
}

func (r *unsettledReconciliation) Reconciliation(string) *Reconciliation {
	return &Reconciliation{Token: r.token, Jobs: []string{"worker"}}
}
func (r *unsettledReconciliation) Acknowledge(string, string) bool { r.acknowledgements++; return true }

func TestAuthorUpdatePublishesOnlySettledStoreOutcomes(t *testing.T) {
	for _, mode := range []string{"store failure", "callback cancellation"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			testutil.FailErr(t, "seed preserved work", store.Set("unsettled", "- [ ] Deliver"))
			before, err := store.Snapshot(t.Context(), "unsettled")
			testutil.FailErr(t, "observe current progress", err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sentinel := errors.New("commit unavailable")
			writer := unsettledProgressStore{Store: store, failure: sentinel}
			if mode == "callback cancellation" {
				writer.cancel = cancel
				sentinel = context.Canceled
			}
			publications := 0
			release := RegisterWriteObserver(func(_ context.Context, event WriteEvent) {
				if event.SessionID == "unsettled" {
					publications++
				}
			})
			defer release()
			reconciliation := &unsettledReconciliation{token: strings.Repeat("a", 64)}
			update, err := PrepareAuthorUpdate("- [x] Deliver")
			testutil.FailErr(t, "prepare completion", err)
			result, err := update.ExpectRevision(before.Revision).ExpectReconciliation(reconciliation.token).Apply(ctx, writer, "unsettled", reconciliation)
			if !errors.Is(err, sentinel) {
				t.Fatalf("unsettled result error: %v", err)
			}
			if result.Updated || result.StateChanged || result.DeliverableClosed || result.Snapshot.Revision != "" || result.Snapshot.Content != "" || result.ReconciliationStatus != "not_requested" {
				t.Fatalf("uncommitted proposal escaped as outcome: %+v", result)
			}
			if publications != 0 || reconciliation.acknowledgements != 0 {
				t.Fatalf("failed commit published events=%d acknowledgements=%d", publications, reconciliation.acknowledgements)
			}
			after, err := store.Snapshot(t.Context(), "unsettled")
			testutil.FailErr(t, "read preserved snapshot", err)
			if before != after {
				t.Fatal("unsettled publication changed stored progress")
			}
		})
	}
}
