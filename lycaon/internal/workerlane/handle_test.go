package workerlane_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type fakeCheckpoints map[string]bool

func (f fakeCheckpoints) VerifyCheckpointReconstructible(_ context.Context, id string) (checkpoint.Manifest, error) {
	if !f[id] {
		return checkpoint.Manifest{}, errors.New("blob missing")
	}
	return checkpoint.Manifest{}, nil
}

func TestHandlesResolveExactlyAndRefuseStaleCheckpoints(t *testing.T) {
	f := newLandingFixture(t)
	h, err := f.lanes.Handle(t.Context(), fakeCheckpoints{"cp-job-1": true}, f.actor, "result-1")
	testutil.FailErr(t, "handle", err)
	if h.CheckpointID != "cp-job-1" || h.JobID != "job-1" || h.Ordinal != 1 || !h.Changed() {
		t.Fatalf("handle = %+v", h)
	}
	_, err = f.lanes.Handle(t.Context(), fakeCheckpoints{}, f.actor, "result-1")
	if !errors.Is(err, workerlane.ErrHandleStale) {
		t.Fatalf("stale checkpoint = %v", err)
	}
	other := workerrecords.Actor{SessionID: "elsewhere", Role: workerrecords.RoleCoordinator, PrincipalID: "elsewhere"}
	_, err = f.lanes.Handle(t.Context(), fakeCheckpoints{"cp-job-1": true}, other, "result-1")
	if !errors.Is(err, workerrecords.ErrResultNotFound) {
		t.Fatalf("another session = %v", err)
	}
}

// A zero-edit round still gets a handle; it carries no checkpoint.
func TestZeroEditRoundHasAHandleWithoutChanges(t *testing.T) {
	f := newLandingFixture(t)
	f.openRound(t, "job-2")
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('result-2', 'job-2', 'complete', '2026-11-12T00:00:00Z')`)
	testutil.FailErr(t, "insert result", err)
	_, err = f.lanes.Handle(t.Context(), fakeCheckpoints{}, f.actor, "result-2")
	if !errors.Is(err, workerlane.ErrResultUnsettled) {
		t.Fatalf("unsettled = %v", err)
	}
	_, err = f.settle(t, &fakeRuntime{}, "job-2", func(context.Context, string) error { return nil })
	testutil.FailErr(t, "settle", err)
	h, err := f.lanes.Handle(t.Context(), fakeCheckpoints{}, f.actor, "result-2")
	testutil.FailErr(t, "handle", err)
	if h.Changed() || h.Ordinal != 2 {
		t.Fatalf("zero-edit handle = %+v", h)
	}
}
