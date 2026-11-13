package workerlane_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type fakeRuntime struct {
	checked    []string
	published  []string
	holders    bool
	publishErr error
	checkpoint string
}

func (r *fakeRuntime) HoldersUnresolved(_ context.Context, root string) (bool, error) {
	r.checked = append(r.checked, root)
	holders := r.holders
	r.holders = false
	return holders, nil
}

func (r *fakeRuntime) PublishCheckpoint(_ context.Context, root string) (string, error) {
	r.published = append(r.published, root)
	err := r.publishErr
	r.publishErr = nil
	return r.checkpoint, err
}

// openRound admits job jobID as the worker's next round without settling it.
func (f *landingFixture) openRound(t *testing.T, jobID string) {
	t.Helper()
	now := db.FormatTime(time.Now().UTC())
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, 'coord', 'child', 'implementer', 'running', 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, now)
	testutil.FailErr(t, "insert job", err)
	_, err = f.records.AdmitRound(t.Context(), f.actor, workerrecords.Op{ID: "round-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
}

func (f *landingFixture) settle(t *testing.T, runtime *fakeRuntime, jobID string, publish func(context.Context, string) error) (workerlane.Settled, error) {
	t.Helper()
	return f.lanes.Settle(t.Context(), runtime, workerlane.SettleRequest{
		Actor: f.actor, Op: workerrecords.Op{ID: "settle-" + jobID}, WorkerID: f.workerID, JobID: jobID, Publish: publish,
	})
}

func TestSettlementPublishesCheckpointThenResultThenLinks(t *testing.T) {
	f := newLandingFixture(t)
	f.openRound(t, "job-2")
	runtime := &fakeRuntime{checkpoint: "cp-round-2"}
	var order []string
	publish := func(_ context.Context, checkpointID string) error {
		order = append(order, "result:"+checkpointID)
		return nil
	}
	settled, err := f.settle(t, runtime, "job-2", publish)
	testutil.FailErr(t, "settle", err)
	if settled.Ordinal != 2 || settled.CheckpointID != "cp-round-2" {
		t.Fatalf("settled = %+v", settled)
	}
	if len(runtime.checked) != 1 || len(runtime.published) != 1 || len(order) != 1 || order[0] != "result:cp-round-2" {
		t.Fatalf("runtime %v / %v, publish %v", runtime.checked, runtime.published, order)
	}
	again, err := f.settle(t, runtime, "job-2", publish)
	testutil.FailErr(t, "settle again", err)
	if again != settled || len(runtime.checked) != 1 || len(order) != 1 {
		t.Fatalf("a settled round was settled again: %+v", again)
	}
}

// A writer still running blocks settlement: no checkpoint, no result, the round
// stays unsettled and a retry completes it.
func TestSettlementBlockedByHoldersPublishesNothing(t *testing.T) {
	f := newLandingFixture(t)
	f.openRound(t, "job-2")
	runtime := &fakeRuntime{checkpoint: "cp-2", holders: true}
	published := 0
	publish := func(context.Context, string) error { published++; return nil }
	_, err := f.settle(t, runtime, "job-2", publish)
	if !errors.Is(err, workerlane.ErrSettlementBlocked) || published != 0 || len(runtime.published) != 0 {
		t.Fatalf("blocked settlement = %v, published %d", err, published)
	}
	assertUnsettled(t, f, "job-2")
	_, err = f.settle(t, runtime, "job-2", publish)
	testutil.FailErr(t, "retry", err)
	if published != 1 {
		t.Fatalf("published = %d", published)
	}
}

// A crash after the checkpoint and before or during the result publication
// leaves the round unsettled; recovery repeats the idempotent steps.
func TestSettlementRecoversAfterACrashAtEachBoundary(t *testing.T) {
	for _, boundary := range []string{"checkpoint", "result"} {
		t.Run(boundary, func(t *testing.T) {
			f := newLandingFixture(t)
			f.openRound(t, "job-2")
			runtime := &fakeRuntime{checkpoint: "cp-2"}
			if boundary == "checkpoint" {
				runtime.publishErr = errors.New("crash at checkpoint publication")
			}
			results := 0
			publish := func(_ context.Context, checkpointID string) error {
				if boundary == "result" && results == 0 {
					results++
					return errors.New("crash at result commit")
				}
				results++
				if checkpointID != "cp-2" {
					t.Fatalf("result published against %q", checkpointID)
				}
				return nil
			}
			if _, err := f.settle(t, runtime, "job-2", publish); err == nil {
				t.Fatal("crash ignored")
			}
			assertUnsettled(t, f, "job-2")
			settled, err := f.settle(t, runtime, "job-2", publish)
			testutil.FailErr(t, "recover", err)
			if settled.CheckpointID != "cp-2" {
				t.Fatalf("settled = %+v", settled)
			}
		})
	}
}

func assertUnsettled(t *testing.T, f *landingFixture, jobID string) {
	t.Helper()
	rounds, err := f.records.Rounds(t.Context(), f.actor, f.workerID)
	testutil.FailErr(t, "rounds", err)
	for _, r := range rounds {
		if r.JobID == jobID && r.SettledAt != "" {
			t.Fatalf("round %s settled: %+v", jobID, r)
		}
	}
}
