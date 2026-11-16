package workerops_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type stateCell struct {
	name      string
	wantState workerstate.State
	wantWhy   workerstate.Reason
	setup     func(*testing.T) (*fixture, string)
}

func stateMatrixCells() []stateCell {
	return []stateCell{
		{
			name: "planned", wantState: workerstate.Planned, wantWhy: workerstate.ReasonNone,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				w, err := f.records.CreateWorker(t.Context(), f.coord, op("create-planned"), workerrecords.WorkerSpec{
					Name: "planned-leg", AgentType: "implementer", ScopeMode: "write", DelegationID: "del-1", LegID: "leg-1",
				})
				testutil.FailErr(t, "create planned", err)
				f.workerID = w.WorkerID
				return f, ""
			},
		},
		{
			name: "waiting_prerequisite", wantState: workerstate.Waiting, wantWhy: workerstate.ReasonPrerequisite,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.job(t, "job-1", api.WorkerStatusHeld, "")
				_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit-1"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
				testutil.FailErr(t, "admit", err)
				return f, ""
			},
		},
		{
			name: "working_active", wantState: workerstate.Working, wantWhy: workerstate.ReasonNone,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-0", "complete", "cp-0")
				f.job(t, "job-1", api.WorkerStatusRunning, "")
				_, err := f.records.StartRound(t.Context(), f.coord, op("start-1"), f.workerID, workerrecords.RoundStart{JobID: "job-1", ExecutionTarget: "local", Instruction: "continue"})
				testutil.FailErr(t, "start", err)
				return f, "result-job-0"
			},
		},
		{
			name: "working_queued", wantState: workerstate.Working, wantWhy: workerstate.ReasonQueued,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.job(t, "job-1", api.WorkerStatusPending, "")
				_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit-1"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
				testutil.FailErr(t, "admit", err)
				return f, ""
			},
		},
		{
			name: "working_parked", wantState: workerstate.Working, wantWhy: workerstate.ReasonParked,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.job(t, "job-1", api.WorkerStatusWaiting, "")
				_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit-1"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
				testutil.FailErr(t, "admit", err)
				return f, ""
			},
		},
		{
			name: "working_approval_wait", wantState: workerstate.Working, wantWhy: workerstate.ReasonApprovalWait,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.svc.Reader.ApprovalWait = func(_ context.Context, child string) bool { return child == "child" }
				f.job(t, "job-1", api.WorkerStatusRunning, "")
				_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit-1"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
				testutil.FailErr(t, "admit", err)
				return f, ""
			},
		},
		{
			name: "asking", wantState: workerstate.Asking, wantWhy: workerstate.ReasonNone,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-0", "complete", "cp-0")
				f.job(t, "job-1", api.WorkerStatusHeld, "")
				_, err := f.records.StartRound(t.Context(), f.coord, op("start-1"), f.workerID, workerrecords.RoundStart{JobID: "job-1", ExecutionTarget: "local", Instruction: "continue"})
				testutil.FailErr(t, "start", err)
				f.dec["child"] = api.WorkerDecisionRequest{ChildSessionID: "child", WorkerID: "job-1", Question: "which encoding?"}
				return f, "result-job-0"
			},
		},
		{
			name: "delivered_unaccounted_open_changes", wantState: workerstate.Delivered, wantWhy: workerstate.ReasonUnaccounted,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "cp-1")
				return f, "result-job-1"
			},
		},
		{
			name: "delivered_accounted_open_changes", wantState: workerstate.Delivered, wantWhy: workerstate.ReasonChangesOpen,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "cp-1")
				_, err := f.records.AccountResult(t.Context(), f.coord, op("account-1"), "result-job-1")
				testutil.FailErr(t, "account", err)
				return f, "result-job-1"
			},
		},
		{
			name: "idle_no_changes", wantState: workerstate.Idle, wantWhy: workerstate.ReasonNone,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "")
				_, err := f.records.AccountResult(t.Context(), f.coord, op("account-1"), "result-job-1")
				testutil.FailErr(t, "account", err)
				return f, "result-job-1"
			},
		},
		{
			name: "idle_shelved_changes", wantState: workerstate.Idle, wantWhy: workerstate.ReasonNone,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "cp-1")
				_, err := f.svc.Shelve(t.Context(), f.coord, op("shelve-1"), "result-job-1", "hold for freeze")
				testutil.FailErr(t, "shelve", err)
				return f, "result-job-1"
			},
		},
		{
			name: "failed_with_changes", wantState: workerstate.Failed, wantWhy: workerstate.ReasonNoReport,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "", "cp-1")
				return f, "result-job-1"
			},
		},
		{
			name: "failed_without_changes", wantState: workerstate.Failed, wantWhy: workerstate.ReasonNoReport,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "", "")
				return f, "result-job-1"
			},
		},
		{
			name: "recovering", wantState: workerstate.Recovering, wantWhy: workerstate.ReasonRecovery,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "cp-1")
				f.svc.Reader.Readiness = func(context.Context, workerrecords.Actor, string) (bool, error) { return true, nil }
				return f, "result-job-1"
			},
		},
		{
			name: "closed_session_unusable", wantState: workerstate.Closed, wantWhy: workerstate.ReasonSessionUnusable,
			setup: func(t *testing.T) (*fixture, string) {
				f := newFixture(t)
				f.round(t, "job-1", "complete", "cp-1")
				_, err := f.db.ExecContext(t.Context(), `UPDATE workers SET child_session_id = NULL WHERE id = ?`, f.workerID)
				testutil.FailErr(t, "clear child session", err)
				return f, "result-job-1"
			},
		},
	}
}

func TestStateOperationAdmissionMatrix(t *testing.T) {
	for _, cell := range stateMatrixCells() {
		t.Run(cell.name, func(t *testing.T) {
			f, handle := cell.setup(t)
			w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
			testutil.FailErr(t, "worker", err)
			snap, err := f.svc.Reader.Read(t.Context(), f.coord, w)
			testutil.FailErr(t, "read", err)
			if snap.View.State != cell.wantState || snap.View.Reason != cell.wantWhy {
				t.Fatalf("view = %+v, want state=%s reason=%s", snap.View, cell.wantState, cell.wantWhy)
			}

			page, err := f.svc.HistoryPaged(t.Context(), f.coord, w.Name, workerops.HistoryQuery{Limit: 8})
			testutil.FailErr(t, "history", err)
			if page.State.State != cell.wantState {
				t.Fatalf("history state = %s, want %s", page.State.State, cell.wantState)
			}

			verifyAskingCell(t, cell, snap.View)
			verifyChangeCells(t, cell, snap.View, handle)
			verifyStopCell(t, cell, snap.View)
			verifyInstructCell(t, cell, snap.View)
		})
	}
}

func verifyAskingCell(t *testing.T, cell stateCell, view workerstate.View) {
	t.Helper()
	f, _ := cell.setup(t)
	w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "worker", err)
	jobID, unlock, err := f.svc.Asking(t.Context(), f.coord, w.Name)
	if view.Admits(workerstate.OpAnswer) {
		testutil.FailErr(t, "asking admitted", err)
		unlock()
		if jobID == "" {
			t.Fatal("expected suspended job id")
		}
		return
	}
	assertRefusalMatchesView(t, err, view, workerstate.OpAnswer)
}

func verifyChangeCells(t *testing.T, cell stateCell, view workerstate.View, handle string) {
	t.Helper()
	if handle == "" {
		return
	}
	for _, opKind := range []workerstate.Operation{workerstate.OpPreview, workerstate.OpPromote, workerstate.OpReject} {
		f, h := cell.setup(t)
		ref, unlock, err := f.svc.Result(t.Context(), f.coord, h, opKind)
		if view.Admits(opKind) {
			testutil.FailErr(t, string(opKind)+" admitted", err)
			unlock()
			if ref.ResultID != h {
				t.Fatalf("result ref = %+v, want %s", ref, h)
			}
			continue
		}
		assertRefusalMatchesView(t, err, view, opKind)
	}
	f, h := cell.setup(t)
	d, err := f.svc.Shelve(t.Context(), f.coord, op("shelve-matrix"), h, "defer landing")
	if view.Admits(workerstate.OpShelve) {
		testutil.FailErr(t, "shelve admitted", err)
		if d.ChangeState != workerrecords.ChangeShelved {
			t.Fatalf("shelve disposition = %+v", d)
		}
		return
	}
	if cell.name == "failed_without_changes" || cell.name == "idle_no_changes" {
		assertRefusalMatchesView(t, err, view, workerstate.OpShelve)
		return
	}
	assertRefusalMatchesView(t, err, view, workerstate.OpShelve)
}

func verifyStopCell(t *testing.T, cell stateCell, view workerstate.View) {
	t.Helper()
	f, _ := cell.setup(t)
	w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "worker", err)
	stop, err := f.svc.Stop(t.Context(), f.coord, op("stop-matrix"), w.Name, "stop test", func(_ context.Context, jobID string) error {
		if round, ok, rerr := f.records.RoundByJob(t.Context(), jobID); rerr == nil && ok && round.SettledAt == "" {
			_, serr := db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
				SettledAt: db.NullString(now()), JobID: jobID,
			})
			return serr
		}
		return nil
	})
	if view.Admits(workerstate.OpCancel) || view.Admits(workerstate.OpWithdraw) {
		testutil.FailErr(t, "stop admitted", err)
		if view.Admits(workerstate.OpWithdraw) != stop.Withdrawn {
			t.Fatalf("stop = %+v for view %+v", stop, view)
		}
		return
	}
	assertRefusalMatchesView(t, err, view, workerstate.OpCancel)
}

func verifyInstructCell(t *testing.T, cell stateCell, view workerstate.View) {
	t.Helper()
	f, _ := cell.setup(t)
	w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "worker", err)
	receipt, err := f.svc.Instruct(t.Context(), f.coord, op("instruct-matrix"), workerops.Instruction{
		Worker: w.Name, Instructions: "inspect caller",
	}, f.enqueue("job-next"))
	if view.Admits(workerstate.OpInstruct) {
		testutil.FailErr(t, "instruct admitted", err)
		wantOutcome := workerops.OutcomeStarted
		if view.State == workerstate.Working {
			wantOutcome = workerops.OutcomeQueued
		}
		if receipt.Outcome != wantOutcome {
			t.Fatalf("receipt = %+v, want outcome %s", receipt, wantOutcome)
		}
		return
	}
	assertRefusalMatchesView(t, err, view, workerstate.OpInstruct)
}

func assertRefusalMatchesView(t *testing.T, err error, view workerstate.View, wantOp workerstate.Operation) {
	t.Helper()
	var refusal *workerops.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want *workerops.Refusal for %s in %+v", err, wantOp, view)
	}
	if refusal.Op != wantOp || refusal.View.State != view.State || refusal.View.Reason != view.Reason {
		t.Fatalf("refusal = %+v, want op=%s view=%+v", refusal, wantOp, view)
	}
	if !slices.Equal(refusal.View.Ops, view.Ops) {
		t.Fatalf("refusal recovery ops = %v, want %v", refusal.View.Ops, view.Ops)
	}
}

func TestOperationConflictAndLostReceiptReplayAcrossTransitions(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "cp-1")

	// Reusing an operation ID already committed to worker creation conflicts.
	_, err := f.svc.Instruct(t.Context(), f.coord, op("create"), workerops.Instruction{
		Worker: "surveyor", Instructions: "conflict op",
	}, f.enqueue("unused"))
	if !errors.Is(err, workerrecords.ErrOperationConflict) {
		t.Fatalf("conflicting op err = %v, want ErrOperationConflict", err)
	}

	// Shelving is idempotent under replay and conflicts when reused by instruct.
	firstShelve, err := f.svc.Shelve(t.Context(), f.coord, op("op-shelve"), "result-job-1", "wait on schema")
	testutil.FailErr(t, "shelve", err)
	replayShelve, err := f.svc.Shelve(t.Context(), f.coord, op("op-shelve"), "result-job-1", "wait on schema")
	testutil.FailErr(t, "replay shelve", err)
	if !reflect.DeepEqual(replayShelve, firstShelve) {
		t.Fatalf("shelve replay = %+v, want %+v", replayShelve, firstShelve)
	}
	_, err = f.svc.Instruct(t.Context(), f.coord, op("op-shelve"), workerops.Instruction{
		Worker: "surveyor", Instructions: "reuse shelve op",
	}, f.enqueue("unused"))
	if !errors.Is(err, workerrecords.ErrOperationConflict) {
		t.Fatalf("reusing shelve op on instruct = %v, want ErrOperationConflict", err)
	}

	// Start round 2, queue an instruction, then settle round 2 before replaying
	// both lost receipts: neither replay admits a round or appends a message.
	started, err := f.svc.Instruct(t.Context(), f.coord, op("op-start-2"), workerops.Instruction{
		Worker: "surveyor", Instructions: "round two",
	}, f.enqueue("job-2"))
	testutil.FailErr(t, "start round 2", err)
	f.jobs["job-2"].Status = api.WorkerStatusRunning

	queued, err := f.svc.Instruct(t.Context(), f.coord, op("op-queue-2"), workerops.Instruction{
		Worker: "surveyor", Instructions: "queued follow-up",
	}, f.enqueue("unused"))
	testutil.FailErr(t, "queue on round 2", err)

	f.settle(t, "job-2", "complete", "cp-2")
	roundsBefore, err := f.records.Rounds(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "rounds before replay", err)

	startedReplay, err := f.svc.Instruct(t.Context(), f.coord, op("op-start-2"), workerops.Instruction{
		Worker: "surveyor", Instructions: "round two",
	}, f.enqueue("job-3"))
	testutil.FailErr(t, "replay start after settle", err)
	queuedReplay, err := f.svc.Instruct(t.Context(), f.coord, op("op-queue-2"), workerops.Instruction{
		Worker: "surveyor", Instructions: "queued follow-up",
	}, f.enqueue("job-4"))
	testutil.FailErr(t, "replay queue after settle", err)

	if startedReplay != started || queuedReplay != queued {
		t.Fatalf("replays = (%+v, %+v), want (%+v, %+v)", startedReplay, queuedReplay, started, queued)
	}
	roundsAfter, err := f.records.Rounds(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "rounds after replay", err)
	if len(roundsAfter) != len(roundsBefore) || len(f.enqueued) != 1 {
		t.Fatalf("rounds = %d (was %d), enqueued = %d", len(roundsAfter), len(roundsBefore), len(f.enqueued))
	}
}

func TestAgentTransitionRefusalAndResolutionMatrix(t *testing.T) {
	// Failed write round with kept changes refuses write-to-read until resolved,
	// then admits write-to-read once changes are rejected.
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")
	_, err := f.svc.Instruct(t.Context(), f.coord, op("to-write"), workerops.Instruction{
		Worker: "surveyor", Instructions: "implement fix", AgentType: "implementer", Brief: "Implement parser fix",
	}, f.enqueue("job-2"))
	testutil.FailErr(t, "to write", err)
	f.settle(t, "job-2", "", "cp-failed")

	if v := f.state(t); v.State != workerstate.Failed || !v.Admits(workerstate.OpPromote) || !v.Admits(workerstate.OpReject) {
		t.Fatalf("failed write state = %+v", v)
	}
	_, err = f.svc.Instruct(t.Context(), f.coord, op("refuse-read"), workerops.Instruction{
		Worker: "surveyor", Instructions: "back to read", AgentType: "repo-researcher",
	}, f.enqueue("unused"))
	var refusal *workerops.Refusal
	if !errors.As(err, &refusal) || refusal.AgentChange != workerstate.AgentChangeOpenChanges || refusal.ChangedResult != "result-job-2" {
		t.Fatalf("expected open-changes refusal naming result-job-2, got %v (%+v)", err, refusal)
	}

	_, err = f.records.SetChange(t.Context(), f.coord, op("reject-failed"), "result-job-2", workerrecords.Change{State: workerrecords.ChangeRejected})
	testutil.FailErr(t, "reject failed changes", err)
	receipt, err := f.svc.Instruct(t.Context(), f.coord, op("admit-read"), workerops.Instruction{
		Worker: "surveyor", Instructions: "verify clean tree", AgentType: "repo-researcher",
	}, f.enqueue("job-3"))
	testutil.FailErr(t, "to read after reject", err)
	if receipt.Outcome != workerops.OutcomeStarted || receipt.Round != 3 {
		t.Fatalf("receipt = %+v", receipt)
	}

	// Planned leg worker seals its agent across rounds.
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child-bound', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now(), now(), now())
	testutil.FailErr(t, "insert child-bound", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, revision, current_phase, created_at, updated_at)
		VALUES ('run-1', 'coord', ?, 'wf', '1.0.0', 'running', 1, 'implement', ?, ?)`, testdbseed.DefaultProjectID, now(), now())
	testutil.FailErr(t, "insert workflow run", err)
	pw, err := f.records.CreateWorker(t.Context(), f.coord, op("create-bound"), workerrecords.WorkerSpec{
		Name: "bound-leg", AgentType: "repo-researcher", ScopeMode: "read", ChildSessionID: "child-bound",
		DelegationID: "del-a", LegID: "leg-a", WorkflowRunID: "run-1", WorkflowPhase: "implement", WorkflowWorkID: "work-leg-a",
	})
	testutil.FailErr(t, "create bound", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('bound-job-1', ?, 'coord', 'child-bound', 'repo-researcher', 'running', 'p', 'b', ?)`, testdbseed.DefaultProjectID, now())
	testutil.FailErr(t, "insert bound job", err)
	_, err = f.records.AdmitRound(t.Context(), f.coord, op("admit-bound-1"), pw.WorkerID, workerrecords.RoundSpec{JobID: "bound-job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit bound", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('result-bound-1', 'bound-job-1', 'complete', ?)`, now())
	testutil.FailErr(t, "insert bound result", err)
	_, err = db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now()), JobID: "bound-job-1",
	})
	testutil.FailErr(t, "settle bound", err)
	f.jobs["bound-job-1"] = &api.WorkerTask{ID: "bound-job-1", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}

	_, err = f.svc.Instruct(t.Context(), f.coord, op("change-bound"), workerops.Instruction{
		Worker: "bound-leg", Instructions: "edit instead", AgentType: "implementer",
	}, func(_ context.Context, _ workerops.RoundJob) (string, string, error) {
		return "bound-job-" + strconv.Itoa(len(f.enqueued)+1), "local", nil
	})
	if !errors.As(err, &refusal) || refusal.AgentChange != workerstate.AgentChangeSealed || refusal.WorkflowWorkID != "work-leg-a" {
		t.Fatalf("bound agent change = %v (%+v), want sealed role", err, refusal)
	}
}
