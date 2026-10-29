package projectremoval

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRunningRemovalDoesNotBlockStatusOrReplayEffects(t *testing.T) {
	o, _, projectID := fixture(t)
	group := &taskgroup.Group{}
	o.Run = group.Go
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	o.Delete = func(ctx context.Context, _ string, _ bool, report func(wire.ProjectRemovalProgress)) error {
		calls.Add(1)
		report(wire.ProjectRemovalProgress{Phase: "chats", Completed: 2, Total: 5})
		close(started)
		<-release
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Cleanup(func() { unblock(); group.Wait(context.Background()) })
	req := wire.ProjectRemovalRequest{OperationID: uuid.NewString()}
	result, err := o.Remove(ctx, projectID, req)
	testutil.FailErr(t, "accept removal", err)
	if result.Progress == nil {
		t.Fatal("accepted work must expose progress")
	}
	<-started
	cancel()
	status := make(chan wire.ProjectRemovalResult, 1)
	go func() { result, _ := o.Result(t.Context(), projectID, req.OperationID); status <- result }()
	select {
	case result = <-status:
		if result.Progress == nil || result.Progress.Completed != 2 || result.Progress.Total != 5 || result.FailureCode != "" {
			t.Fatalf("running status = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("status waited for deletion")
	}
	retry, err := o.Remove(t.Context(), projectID, req)
	testutil.FailErr(t, "retry running operation", err)
	if retry.Progress == nil || calls.Load() != 1 {
		t.Fatalf("retry = %+v, calls = %d", retry, calls.Load())
	}
	row, found, err := o.Store.read(t.Context(), req.OperationID)
	testutil.FailErr(t, "read journal", err)
	if !found || row.Settled || row.Result.Progress != nil {
		t.Fatalf("live progress leaked into durable outcome: %+v", row)
	}
	// The runner retains server authority after the initiating request disconnects.
	unblock()
	group.Wait(t.Context())
	settled, err := o.Result(t.Context(), projectID, req.OperationID)
	testutil.FailErr(t, "settled outcome", err)
	if settled.Progress != nil || settled.ProjectState != "deleted" {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestShutdownRecordsObservedOutcome(t *testing.T) {
	o, _, projectID := fixture(t)
	group := &taskgroup.Group{}
	o.Run = group.Go
	started := make(chan struct{})
	o.Delete = func(ctx context.Context, _ string, _ bool, _ func(wire.ProjectRemovalProgress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	req := wire.ProjectRemovalRequest{OperationID: uuid.NewString()}
	_, err := o.Remove(t.Context(), projectID, req)
	testutil.FailErr(t, "accept removal", err)
	<-started
	group.Stop()
	group.Wait(t.Context())
	// The runner's cancellation stops the work, not the receipt that records what it observed.
	settled, err := o.Result(t.Context(), projectID, req.OperationID)
	testutil.FailErr(t, "settled outcome", err)
	if settled.Progress != nil || settled.ProjectState != "retained" || settled.FailureCode != "project_removal_failed" {
		t.Fatalf("settled = %+v", settled)
	}
	row, found, err := o.Store.read(t.Context(), req.OperationID)
	testutil.FailErr(t, "read journal", err)
	if !found || !row.Settled {
		t.Fatalf("shutdown left the receipt unsettled: %+v", row)
	}
}
