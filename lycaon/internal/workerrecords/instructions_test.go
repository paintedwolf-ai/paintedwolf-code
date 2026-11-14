package workerrecords_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// A coordinator instructs only a running round; a stopped worker is started
// with StartRound.
func TestInstructionsNeedARunningRound(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.settle(t, "job-1", "")
	_, err := f.store.AppendMessage(t.Context(), f.coord, op("late"), w.WorkerID, workerrecords.MessageSpec{Body: "also the caller"})
	wantErr(t, "instruct a stopped worker", err, workerrecords.ErrNoRunningRound)
}

// The starting instruction travels in the round's prompt, so the round's
// pages carry only what was queued after it; an instruction that missed its
// round is not applied and never reaches a later round.
func TestRoundsCarryOnlyTheirOwnInstructions(t *testing.T) {
	f := newFixture(t)
	w, child := f.workerWithRound(t, "fixer", "child-1", "job-1")
	_, err := f.store.AppendMessage(t.Context(), f.coord, op("queued"), w.WorkerID, workerrecords.MessageSpec{Body: "check the encoder too"})
	testutil.FailErr(t, "queue instruction", err)
	f.settle(t, "job-1", "")

	missed, err := f.store.NotApplied(t.Context(), f.coord, w.WorkerID, 1)
	testutil.FailErr(t, "not applied", err)
	if len(missed) != 1 || missed[0].Body != "check the encoder too" {
		t.Fatalf("not applied = %+v", missed)
	}
	page, err := f.store.OpenPage(t.Context(), child, w.WorkerID, 0, workerrecords.MaxPageMessages)
	testutil.FailErr(t, "page between rounds", err)
	if len(page.Messages) != 0 {
		t.Fatalf("stopped worker got a page: %+v", page.Messages)
	}

	f.insertJob(t, "job-2", coordSession, "child-1", "pending")
	start, err := f.store.StartRound(t.Context(), f.coord, op("start-2"), w.WorkerID, workerrecords.RoundStart{JobID: "job-2", ExecutionTarget: "local", Instruction: "add empty-input coverage"})
	testutil.FailErr(t, "start round", err)
	if start.Ordinal != 2 || start.InstructionSeq != 2 {
		t.Fatalf("start = %+v", start)
	}
	_, err = f.store.AppendMessage(t.Context(), f.coord, op("follow-up"), w.WorkerID, workerrecords.MessageSpec{Body: "keep the old test name"})
	testutil.FailErr(t, "queue follow-up", err)
	page, err = f.store.OpenPage(t.Context(), child, w.WorkerID, 0, workerrecords.MaxPageMessages)
	testutil.FailErr(t, "round 2 page", err)
	if len(page.Messages) != 1 || page.Messages[0].Body != "keep the old test name" || page.Messages[0].RoundOrdinal != 2 {
		t.Fatalf("round 2 page = %+v", page.Messages)
	}
	testutil.FailErr(t, "commit", f.store.CommitPage(t.Context(), child, w.WorkerID, 0, page.ID))
	f.settle(t, "job-2", "")
	missed, err = f.store.NotApplied(t.Context(), f.coord, w.WorkerID, 2)
	testutil.FailErr(t, "round 2 not applied", err)
	if len(missed) != 0 {
		t.Fatalf("delivered instruction reported not applied: %+v", missed)
	}
}

// A brief or agent change commits with the round, and the child session's
// agent changes with the worker's.
func TestStartRoundAppliesProfileChanges(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.settle(t, "job-1", "")
	f.insertJob(t, "job-2", coordSession, "child-1", "pending")
	start := workerrecords.RoundStart{JobID: "job-2", ExecutionTarget: "local", Instruction: "implement what you found",
		Brief: "Survey the parser", AgentType: "repo-researcher", ScopeMode: "read"}
	first, err := f.store.StartRound(t.Context(), f.coord, op("start-2"), w.WorkerID, start)
	testutil.FailErr(t, "start", err)
	again, err := f.store.StartRound(t.Context(), f.coord, op("start-2"), w.WorkerID, start)
	testutil.FailErr(t, "replay", err)
	if again != first {
		t.Fatalf("replay = %+v, first = %+v", again, first)
	}
	got, err := f.store.Worker(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "worker", err)
	if got.AgentType != "repo-researcher" || got.ScopeMode != "read" || got.Brief != "Survey the parser" {
		t.Fatalf("worker = %+v", got)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM sessions WHERE id = 'child-1' AND agent_type = 'repo-researcher'`); n != 1 {
		t.Fatal("child session kept its old agent")
	}
	_, err = f.store.StartRound(t.Context(), f.coord, op("start-3"), w.WorkerID, workerrecords.RoundStart{JobID: "job-2", ExecutionTarget: "local", Instruction: "x"})
	wantErr(t, "second round while one runs", err, workerrecords.ErrRoundUnsettled)
	_, err = f.store.StartRound(t.Context(), f.coord, op("start-4"), w.WorkerID, workerrecords.RoundStart{JobID: "job-3", ExecutionTarget: "local", Instruction: "x", AgentType: "implementer"})
	wantErr(t, "agent without scope", err, workerrecords.ErrInvalidProfile)
}

// A job names its worker from admission and its result handle once settled;
// a job that runs no worker round names nothing.
func TestNamesByJobFollowsTheRound(t *testing.T) {
	f := newFixture(t)
	f.workerWithRound(t, "fixer", "child-1", "job-1")
	names, ok, err := f.store.NamesByJob(t.Context(), "job-1")
	testutil.FailErr(t, "names while running", err)
	if !ok || names.Worker != "fixer" || names.Handle != "" {
		t.Fatalf("running names = %+v ok=%v", names, ok)
	}
	f.insertResult(t, "result-1", "job-1")
	f.settle(t, "job-1", "")
	names, ok, err = f.store.NamesByJob(t.Context(), "job-1")
	testutil.FailErr(t, "names once settled", err)
	if !ok || names.Worker != "fixer" || names.Handle != "result-1" {
		t.Fatalf("settled names = %+v ok=%v", names, ok)
	}
	if _, ok, err := f.store.NamesByJob(t.Context(), "job-other"); ok || err != nil {
		t.Fatalf("job without a round: ok=%v err=%v", ok, err)
	}
}

// A later round's frame carries the brief, every instruction the round has
// received, how its workspace was prepared and what became of its last
// changes; a first round's frame carries its brief alone.
func TestRoundFrameCarriesTheRoundsStartingPoint(t *testing.T) {
	f := newFixture(t)
	w, child := f.workerWithRound(t, "fixer", "child-1", "job-1")
	first, ok, err := f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "first frame", err)
	if !ok || first.Ordinal != 1 || first.Workspace != "" || len(first.Instructions) != 0 {
		t.Fatalf("first frame = %+v ok=%v", first, ok)
	}
	f.insertResult(t, "result-1", "job-1")
	f.settle(t, "job-1", "cp-1")
	_, err = f.store.SetChange(t.Context(), f.coord, op("reject-1"), "result-1", workerrecords.Change{State: workerrecords.ChangeRejected})
	testutil.FailErr(t, "reject", err)
	f.insertJob(t, "job-2", coordSession, "child-1", "pending")
	_, err = f.store.StartRound(t.Context(), f.coord, op("start-2"), w.WorkerID, workerrecords.RoundStart{JobID: "job-2", ExecutionTarget: "local", Instruction: "try the other parser"})
	testutil.FailErr(t, "start round 2", err)
	_, err = f.store.RecordPreparation(t.Context(), f.coord, op("prep-2"), w.WorkerID, workerrecords.Preparation{PreparationRef: "/lanes/fixer.2", BindingGeneration: 2})
	testutil.FailErr(t, "prepare", err)
	_, err = f.store.AppendMessage(t.Context(), f.coord, op("mid-round"), w.WorkerID, workerrecords.MessageSpec{Body: "keep the old tests"})
	testutil.FailErr(t, "mid-round instruction", err)
	frame, ok, err := f.store.RoundFrame(t.Context(), child)
	testutil.FailErr(t, "second frame", err)
	if !ok || frame.Ordinal != 2 || frame.PriorChange != workerrecords.ChangeRejected || frame.Workspace != workerrecords.WorkspaceReset {
		t.Fatalf("second frame = %+v ok=%v", frame, ok)
	}
	if len(frame.Instructions) != 2 || frame.Instructions[0].Body != "try the other parser" || frame.Instructions[1].Body != "keep the old tests" {
		t.Fatalf("instructions = %+v", frame.Instructions)
	}
}
