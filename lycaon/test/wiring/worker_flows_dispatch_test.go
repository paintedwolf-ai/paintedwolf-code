package wiring

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Flow 1. Six read workers are dispatched and never touched again: each
// delivers one result with no changes, and one checklist update accounts for
// all six.
func TestFlowReadFanOutNeedsNothingPerWorkerAfterDispatch(t *testing.T) {
	f := newWorkerFlow(t, "flow-read-fanout")
	f.seed("auth.go", "package auth\n")
	names := make([]string, 6)
	for i := range names {
		names[i] = fmt.Sprintf("auth-%d", i+1)
		f.scripts.push("path-explorer", reads("auth.go"), delivers("complete", "Surveyed one auth subsystem."))
		f.start(names[i], "path-explorer", "Survey one auth subsystem.")
	}
	for _, name := range names {
		f.wantState(name, workerstate.Working, workerstate.ReasonQueued)
	}
	f.run("wait", map[string]any{"conditions": []any{map[string]any{"kind": "all_workers_idle"}}})
	f.drain()

	for _, name := range names {
		snap := f.wantState(name, workerstate.Delivered, workerstate.ReasonUnaccounted)
		if round := f.result(name, 1); len(snap.Rounds) != 1 || round.CheckpointID != "" {
			t.Fatalf("read worker %s: rounds %+v, want one result without changes", name, snap.Rounds)
		}
	}
	f.account("all six subsystems surveyed")
	for _, name := range names {
		if snap := f.wantState(name, workerstate.Idle, workerstate.ReasonNone); len(snap.Rounds) != 1 {
			t.Fatalf("worker %s ran %d rounds, want the one it was dispatched for", name, len(snap.Rounds))
		}
	}
}

// Flow 2. A write worker is corrected once: it keeps its conversation and
// workspace, the instruction accounts for the first result, and landing the
// second result lands the first with it.
func TestFlowOneWriteWorkerCorrectedOnce(t *testing.T) {
	f := newWorkerFlow(t, "flow-corrected-once")
	const seed, first, second = "package parser\n", "package parser\n\nfunc Parse(s string) bool { return len(s) > 0 }\n",
		"package parser\n\nfunc Parse(s string) bool { return s != \"\" }\n"
	f.seed("parser.go", seed)

	f.scripts.push("implementer", writes("parser.go", first), delivers("complete", "Fixed the parser."))
	f.start("parser-fix", "implementer", "Fix the parser failure.", "parser.go")
	f.drain()
	delivered := f.wantState("parser-fix", workerstate.Delivered, workerstate.ReasonUnaccounted)
	r1 := f.result("parser-fix", 1)
	if r1.CheckpointID == "" {
		t.Fatalf("round 1 has no checkpoint for its changes: %+v", r1)
	}
	f.wantPrimary("parser.go", seed)
	lane := f.lane("parser-fix")

	f.scripts.push("implementer", writes("parser.go", second), delivers("complete", "Covered empty input."))
	f.run("instruct_worker", map[string]any{"worker": "parser-fix", "instructions": "Add empty-input coverage."})
	if f.disposition("parser-fix", r1.ResultID).AccountedAt == "" {
		t.Fatal("the instruction did not account for the first result")
	}
	f.drain()
	corrected := f.worker("parser-fix")
	r2 := f.result("parser-fix", 2)
	if corrected.Worker.ChildSessionID != delivered.Worker.ChildSessionID {
		t.Fatalf("the correction ran in session %s, not the worker's own %s", corrected.Worker.ChildSessionID, delivered.Worker.ChildSessionID)
	}
	if got := f.lane("parser-fix"); got.Generation != lane.Generation || got.PreparationRef != lane.PreparationRef {
		t.Fatalf("the correction ran in lane %+v, not the kept lane %+v", got, lane)
	}

	f.run("promote_overlay", map[string]any{"overlay_id": r2.ResultID})
	f.wantPrimary("parser.go", second)
	f.wantChange("parser-fix", r2.ResultID, workerrecords.ChangeLanded)
	f.wantChange("parser-fix", r1.ResultID, workerrecords.ChangeLanded)
	f.account("parser fix landed")
	f.wantState("parser-fix", workerstate.Idle, workerstate.ReasonNone)
}

// Flow 3. Three write workers land one at a time. The second conflicts with
// what the first landed; the preview names the path and the coordinator's
// resolution is what lands.
func TestFlowThreeWriteWorkersLandWithOneConflict(t *testing.T) {
	f := newWorkerFlow(t, "flow-three-writers")
	f.seed("shared.go", "package app\n")
	f.seed("extra.go", "package app\n")
	const fromA, fromB, fromC, resolved = "package app\n\nvar A = 1\n", "package app\n\nvar B = 2\n",
		"package app\n\nvar C = 3\n", "package app\n\nvar A = 1\nvar B = 2\n"
	for _, w := range []struct{ name, file, content string }{
		{"api", "shared.go", fromA}, {"store", "shared.go", fromB}, {"cli", "extra.go", fromC},
	} {
		f.scripts.push("implementer", writes(w.file, w.content), delivers("complete", "Edited "+w.file+"."))
		f.start(w.name, "implementer", "Edit "+w.file+".", w.file)
	}
	f.run("wait", map[string]any{"conditions": []any{map[string]any{"kind": "next_worker_done"}}})
	f.drain()
	a, b, c := f.result("api", 1), f.result("store", 1), f.result("cli", 1)

	f.run("promote_overlay", map[string]any{"overlay_id": a.ResultID})
	f.wantPrimary("shared.go", fromA)
	// Landing one result freezes no other worker.
	f.wantState("store", workerstate.Delivered, workerstate.ReasonUnaccounted)
	f.wantState("cli", workerstate.Delivered, workerstate.ReasonUnaccounted)

	var preview api.WorkerMergeResult
	testutil.FailErr(t, "decode preview", json.Unmarshal([]byte(f.run("preview_overlay", map[string]any{"overlay_id": b.ResultID})), &preview))
	if len(preview.Conflicts) != 1 || preview.Conflicts[0].Path != "shared.go" {
		t.Fatalf("preview conflicts = %+v, want shared.go", preview.Conflicts)
	}
	f.wantPrimary("shared.go", fromA)

	f.run("promote_overlay", map[string]any{
		"overlay_id":  b.ResultID,
		"resolutions": []any{map[string]any{"path": "shared.go", "content": resolved}},
	})
	f.wantPrimary("shared.go", resolved)
	f.run("promote_overlay", map[string]any{"overlay_id": c.ResultID})
	f.wantPrimary("extra.go", fromC)
	for name, round := range map[string]workerrecords.Round{"api": a, "store": b, "cli": c} {
		f.wantChange(name, round.ResultID, workerrecords.ChangeLanded)
	}
}

// Flow 4. A worker's blocking question suspends it. An instruction is
// refused while it asks; the answer resumes the same round. Stopping an
// asking worker withdraws the question and fails the round.
func TestFlowBlockingQuestionSuspendsUntilAnswered(t *testing.T) {
	f := newWorkerFlow(t, "flow-question")
	f.seed("main.go", "package main\n")

	f.scripts.push("implementer", asks("May I write the hosts fixture outside the project?", "sandbox"),
		delivers("complete", "Used a project-local fixture."))
	f.start("fixture", "implementer", "Configure the host fixture.", "main.go")
	f.drain()
	asking := f.wantState("fixture", workerstate.Asking, workerstate.ReasonNone)

	rej := f.refused("instruct_worker", map[string]any{"worker": "fixture", "instructions": "Proceed anyway."}, "INSTRUCT_WORKER_ASKING")
	if rej.Data["worker"] != "fixture" {
		t.Fatalf("the refusal names %v, want the asking worker", rej.Data["worker"])
	}

	// The roster the coordinator is shown carries the question it answers.
	_, _, err := f.h.Sessions.Manager.Coordinator.Assembly.WorkerRoster(f.ctx, f.sess.ID)
	testutil.FailErr(t, "render roster", err)
	f.run("answer_decision", map[string]any{"worker": "fixture", "answer": "Use a project-local hosts file.", "resolved_by": "user"})
	f.drain()
	answered := f.wantState("fixture", workerstate.Delivered, workerstate.ReasonUnaccounted)
	if len(answered.Rounds) != 1 || answered.Rounds[0].JobID != asking.Rounds[0].JobID {
		t.Fatalf("the answer started another round: %+v", answered.Rounds)
	}

	f.scripts.push("implementer", asks("Should the fixture cover IPv6?", "decision"))
	f.start("fixture-v6", "implementer", "Extend the fixture.", "main.go")
	f.drain()
	f.wantState("fixture-v6", workerstate.Asking, workerstate.ReasonNone)
	f.run("worker_cancel", map[string]any{"worker": "fixture-v6", "reason": "No longer needed."})
	f.drain()
	stopped := f.worker("fixture-v6")
	if stopped.View.State != workerstate.Failed || stopped.Rounds[0].SettledAt == "" {
		t.Fatalf("a stopped asking worker is %s with rounds %+v, want a failed, settled round", stopped.View.State, stopped.Rounds)
	}
	f.refused("answer_decision", map[string]any{"worker": "fixture-v6", "answer": "Yes.", "resolved_by": "user"}, "DECISION_NOT_FOUND")
}

// Flow 5, begin again. Rejecting the only result accounts for it and the
// next round starts from clean source in the same conversation.
func TestFlowRejectedResultRestartsFromCleanSource(t *testing.T) {
	f := newWorkerFlow(t, "flow-begin-again")
	const seedA, seedB = "package a\n", "package a\n\nvar B = 0\n"
	f.seed("a.go", seedA)
	f.seed("b.go", seedB)

	f.scripts.push("implementer", writes("a.go", "package a\n\nvar Rewritten = true\n"), delivers("complete", "Rewrote the module."))
	f.start("fixer", "implementer", "Make the two-line fix.", "a.go", "b.go")
	f.drain()
	first := f.worker("fixer")
	r1 := f.result("fixer", 1)
	lane := f.lane("fixer")

	f.run("reject_overlay", map[string]any{"overlay_id": r1.ResultID, "reason": "A rewrite, not the two-line fix."})
	f.wantChange("fixer", r1.ResultID, workerrecords.ChangeRejected)
	if f.disposition("fixer", r1.ResultID).AccountedAt == "" {
		t.Fatal("the rejection did not account for the result")
	}

	const fixedB = "package a\n\nvar B = 2\n"
	f.scripts.push("implementer", writes("b.go", fixedB), delivers("complete", "Made the two-line fix."))
	f.run("instruct_worker", map[string]any{"worker": "fixer", "instructions": "Only change B to 2."})
	f.drain()
	again := f.worker("fixer")
	r2 := f.result("fixer", 2)
	if again.Worker.ChildSessionID != first.Worker.ChildSessionID {
		t.Fatal("the worker began again in another conversation")
	}
	if got := f.lane("fixer"); got.Generation <= lane.Generation || got.SourceResultID != r1.ResultID {
		t.Fatalf("lane after the rejection = %+v, want a new generation prepared after %s", got, r1.ResultID)
	}

	f.run("promote_overlay", map[string]any{"overlay_id": r2.ResultID})
	// The rejected rewrite of a.go is not in what landed.
	f.wantPrimary("a.go", seedA)
	f.wantPrimary("b.go", fixedB)
}

// Flow 5, keep round one and drop round two; then drop one file at landing.
// Rejecting the second result returns the lane to the first, which the next
// round builds on.
func TestFlowRejectedSecondRoundReturnsToTheFirst(t *testing.T) {
	f := newWorkerFlow(t, "flow-keep-first")
	const seed = "package a\n"
	f.seed("a.go", seed)
	f.seed("b.go", seed)
	const keptA, droppedB, addedC = "package a\n\nvar A = 1\n", "package a\n\nvar B = 99\n", "package a\n\nvar C = 3\n"

	f.scripts.push("implementer", writes("a.go", keptA), delivers("complete", "Round one."))
	f.start("builder", "implementer", "Build the feature.", "a.go", "b.go", "c.go", "scratch.go")
	f.drain()
	r1 := f.result("builder", 1)
	f.scripts.push("implementer", writes("b.go", droppedB), delivers("complete", "Round two."))
	f.run("instruct_worker", map[string]any{"worker": "builder", "instructions": "Continue with b.go."})
	f.drain()
	r2 := f.result("builder", 2)

	f.run("reject_overlay", map[string]any{"overlay_id": r2.ResultID, "reason": "Round two went wrong."})
	f.wantChange("builder", r2.ResultID, workerrecords.ChangeRejected)
	if got := f.disposition("builder", r1.ResultID).ChangeState; got != "" {
		t.Fatalf("rejecting round two changed round one's changes to %q", got)
	}

	f.scripts.push("implementer", writes("c.go", addedC), writes("scratch.go", "package a\n\nvar Scratch = 1\n"), delivers("complete", "Round three."))
	f.run("instruct_worker", map[string]any{"worker": "builder", "instructions": "Add c.go instead."})
	f.drain()
	r3 := f.result("builder", 3)
	if got := f.lane("builder"); got.SourceResultID != r2.ResultID {
		t.Fatalf("lane = %+v, want one reset after rejecting %s", got, r2.ResultID)
	}

	f.run("promote_overlay", map[string]any{"overlay_id": r3.ResultID, "drop": []any{"scratch.go"}})
	// Round one's work was carried, round two's was not, the dropped file did not land.
	f.wantPrimary("a.go", keptA)
	f.wantPrimary("b.go", seed)
	f.wantPrimary("c.go", addedC)
	if _, landed := f.primary("scratch.go"); landed {
		t.Fatal("a dropped file landed")
	}
	f.wantChange("builder", r1.ResultID, workerrecords.ChangeLanded)
}

// Flow 6. A round that is no longer wanted is stopped and the worker is sent
// in a new direction: two calls, and the worker's conversation and landed
// work survive. Interrupting a tool call mid-run and the report-only turn are
// covered where a round can be held open, in the cancellation tests.
func TestFlowStoppedWorkerIsRedirected(t *testing.T) {
	f := newWorkerFlow(t, "flow-redirect")
	f.seed("a.go", "package a\n")
	const first, fast = "package a\n\nvar Step = 1\n", "package a\n\nvar Step = 1\nvar Fast = true\n"

	f.scripts.push("implementer", writes("a.go", first), delivers("complete", "Finished step one."))
	f.start("runner", "implementer", "Run the migration.", "a.go")
	f.drain()
	started := f.worker("runner")

	f.run("instruct_worker", map[string]any{"worker": "runner", "instructions": "Run the slow path for step two."})
	f.wantState("runner", workerstate.Working, workerstate.ReasonQueued)
	f.run("worker_cancel", map[string]any{"worker": "runner", "reason": "Wrong approach."})
	f.drain()
	stopped := f.worker("runner")
	if stopped.View.State != workerstate.Failed || len(stopped.Rounds) != 2 || stopped.Rounds[1].SettledAt == "" {
		t.Fatalf("a stopped worker is %s with rounds %+v, want a failed, settled second round", stopped.View.State, stopped.Rounds)
	}

	f.scripts.push("implementer", writes("a.go", fast), delivers("complete", "Took the fast path."))
	f.run("instruct_worker", map[string]any{"worker": "runner", "instructions": "Take the fast path instead."})
	f.drain()
	redirected := f.worker("runner")
	if len(redirected.Rounds) != 3 || redirected.Worker.ChildSessionID != started.Worker.ChildSessionID {
		t.Fatalf("redirected worker ran %d rounds in session %s, want a third round in %s",
			len(redirected.Rounds), redirected.Worker.ChildSessionID, started.Worker.ChildSessionID)
	}
	f.run("promote_overlay", map[string]any{"overlay_id": f.result("runner", 3).ResultID})
	// The redirected round built on step one.
	f.wantPrimary("a.go", fast)
}

// Flow 8. A consumer named after a producer waits while the producer is
// corrected and starts only when the producer's changes land. A consumer
// stacked on the producer's result starts at once.
func TestFlowDependentWorkerStartsWhenItsProducerLands(t *testing.T) {
	f := newWorkerFlow(t, "flow-dependent")
	f.seed("schema.go", "package db\n")
	f.seed("caller.go", "package db\n")
	f.seed("report.go", "package db\n")
	const schemaV1, schemaV2, caller = "package db\n\nvar Schema = 1\n", "package db\n\nvar Schema = 2\n", "package db\n\nvar Caller = Schema\n"

	f.scripts.push("implementer", writes("schema.go", schemaV1), delivers("complete", "Changed the schema."))
	f.start("schema", "implementer", "Change the schema.", "schema.go")
	args := TaskToolArgs("implementer", "Update the caller.", "caller.go")
	args["name"], args["after_workers"] = "caller", []any{"schema"}
	f.run("task", args)
	f.wantState("caller", workerstate.Waiting, workerstate.ReasonPrerequisite)

	f.drain()
	r1 := f.result("schema", 1)
	f.wantState("caller", workerstate.Waiting, workerstate.ReasonPrerequisite)

	// Stacked on the unlanded first result, this consumer does not wait.
	stacked := TaskToolArgs("implementer", "Report on the new schema.", "report.go")
	stacked["name"] = "report"
	stacked["scope"].(map[string]any)["base_overlay_id"] = r1.ResultID
	f.scripts.push("implementer", writes("report.go", "package db\n\nvar Reported = Schema\n"), delivers("complete", "Reported."))
	f.run("task", stacked)
	f.wantState("report", workerstate.Working, workerstate.ReasonQueued)
	f.drain()
	f.wantState("report", workerstate.Delivered, workerstate.ReasonUnaccounted)

	f.scripts.push("implementer", writes("schema.go", schemaV2), delivers("complete", "Corrected the schema."))
	f.run("instruct_worker", map[string]any{"worker": "schema", "instructions": "Use version 2."})
	f.drain()
	r2 := f.result("schema", 2)
	if waiting := f.wantState("caller", workerstate.Waiting, workerstate.ReasonPrerequisite); waiting.Rounds[0].SettledAt != "" {
		t.Fatal("the consumer ran before its producer's changes landed")
	}

	f.scripts.push("implementer", writes("caller.go", caller), delivers("complete", "Updated the caller."))
	f.run("promote_overlay", map[string]any{"overlay_id": r2.ResultID})
	f.wantPrimary("schema.go", schemaV2)
	f.drain()
	f.wantState("caller", workerstate.Delivered, workerstate.ReasonUnaccounted)
	f.run("promote_overlay", map[string]any{"overlay_id": f.result("caller", 1).ResultID})
	f.wantPrimary("caller.go", caller)
}
