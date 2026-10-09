package hitl

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
	"time"
)

// Two holders of the same key must never overlap, and entries must be freed
// once the last holder releases, without a blocked waiter acquiring a mutex a
// third caller already replaced.
func TestKeyedMutexSerializesAndCleansUp(t *testing.T) {
	var km keyedMutex
	const workers = 32
	inCritical := 0
	var observed sync.Map
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := km.Lock("checkpoint-1")
			inCritical++
			if inCritical != 1 {
				observed.Store("overlap", true)
			}
			time.Sleep(time.Millisecond)
			inCritical--
			unlock()
		}()
	}
	wg.Wait()
	if _, overlap := observed.Load("overlap"); overlap {
		t.Fatal("two holders overlapped in the critical section")
	}
	km.mu.Lock()
	defer km.mu.Unlock()
	if len(km.entries) != 0 {
		t.Fatalf("entries not cleaned up: %d", len(km.entries))
	}
}

type failingInstaller struct{ err error }

func (f failingInstaller) InstallApprovalOption(context.Context, string, ApprovalOption) (func(), error) {
	return nil, f.err
}

// Authority must exist before a held operation can commit an approval. An
// install failure leaves the checkpoint pending and records no decision.
func TestInstallFailureLeavesCheckpointPending(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "install-fail.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)
	rec := &fakeAuthzRecorder{}
	mgr := NewCheckpoints(NewSQLStore(sqlDB), nil, rec)
	installErr := errors.New("runtime unavailable")
	mgr.Authority.SetApprovalAuthorityInstaller(failingInstaller{err: installErr})

	verdict, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	if verdict != gate.Ask || decision == nil {
		t.Fatalf("gate fixture verdict = %v", verdict)
	}
	resp, err := mgr.RequestCheckpoint(ctx, CheckpointRequest{
		SessionID:      "sess-1",
		Kind:           api.CheckpointKindToolApproval,
		Decision:       decision,
		ProposedAction: &ProposedAction{Tool: "write", SessionID: "sess-1"},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	_, err = mgr.Authority.ResolveApprovalOption(ctx, "sess-1", resp.CheckpointID, "approve_current_action")
	if !errors.Is(err, installErr) {
		t.Fatalf("resolve err = %v, want install failure", err)
	}
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "PollCheckpoint", err)
	if final.Status != DecisionStatusPending {
		t.Fatalf("checkpoint status = %q want pending", final.Status)
	}
	if len(rec.gateTx) != 0 {
		t.Fatalf("decision must not be sealed = %+v", rec.gateTx)
	}
	if len(rec.capability) != 0 {
		t.Fatalf("capability records = %+v", rec.capability)
	}
}
