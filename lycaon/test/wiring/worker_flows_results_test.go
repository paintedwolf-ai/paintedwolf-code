package wiring

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Flow 7. A round that ends without a report fails, and what it changed is
// kept under a result handle the coordinator can preview and shelve. Reading
// the worker's history acknowledges nothing.
func TestFlowFailedRoundKeepsItsWorkAndHistoryReadsQuietly(t *testing.T) {
	f := newWorkerFlow(t, "flow-kept-work")
	f.seed("long.go", "package long\n")
	const kept = "package long\n\nvar Kept = true\n"

	f.scripts.push("implementer", writes("long.go", kept),
		api.ToolCall{ID: "note", Name: "message_coordinator", Args: map[string]any{"text": "The migration is larger than the brief says."}})
	f.start("long-runner", "implementer", "Run the long edit.", "long.go")
	f.drain()
	failed := f.wantState("long-runner", workerstate.Failed, workerstate.ReasonNoReport)
	round := f.result("long-runner", 1)
	if round.CheckpointID == "" {
		t.Fatalf("the failed round kept no checkpoint: %+v", round)
	}
	f.wantPrimary("long.go", "package long\n")

	unread := func() int {
		n, err := f.h.Workers.Records.UnreadCoordinatorCount(f.ctx, f.actor, failed.Worker.ID)
		testutil.FailErr(t, "unread count", err)
		return n
	}
	before := unread()
	if before != 1 {
		t.Fatalf("unread messages from the worker = %d, want its one note", before)
	}
	f.run("worker_history", map[string]any{"worker": "long-runner"})
	if after := unread(); after != before {
		t.Fatalf("reading history left %d unread messages, want %d", after, before)
	}

	var preview api.WorkerMergeResult
	testutil.FailErr(t, "decode preview", json.Unmarshal([]byte(f.run("preview_overlay", map[string]any{"overlay_id": round.ResultID})), &preview))
	if len(preview.Conflicts) != 0 {
		t.Fatalf("kept work previews with conflicts %+v", preview.Conflicts)
	}
	f.run("shelve_overlay", map[string]any{"overlay_id": round.ResultID, "reason": "Hold what the failed round kept."})
	f.wantChange("long-runner", round.ResultID, workerrecords.ChangeShelved)
	f.run("promote_overlay", map[string]any{"overlay_id": round.ResultID})
	f.wantPrimary("long.go", kept)
}

// Flow 9. Shelved changes stay shelved and visible on the roster with their
// reason; landing them later runs no round.
func TestFlowShelvedChangesWaitForALaterLanding(t *testing.T) {
	f := newWorkerFlow(t, "flow-shelved")
	f.seed("fix.go", "package fix\n")
	const ready = "package fix\n\nvar Ready = true\n"
	const reason = "Hold until the release window opens."

	f.scripts.push("implementer", writes("fix.go", ready), delivers("complete", "Prepared the fix."))
	f.start("parser-fix", "implementer", "Prepare the fix.", "fix.go")
	f.drain()
	r1 := f.result("parser-fix", 1)

	f.run("shelve_overlay", map[string]any{"overlay_id": r1.ResultID, "reason": reason})
	shelved := f.disposition("parser-fix", r1.ResultID)
	if shelved.ChangeState != workerrecords.ChangeShelved || shelved.ShelveReason != reason || shelved.AccountedAt == "" {
		t.Fatalf("disposition = %+v, want shelved, accounted, with its reason", shelved)
	}
	// Shelving resolves the changes: nothing is outstanding for closeout.
	f.wantState("parser-fix", workerstate.Idle, workerstate.ReasonNone)
	f.wantPrimary("fix.go", "package fix\n")

	var board api.BoardView
	testutil.FailErr(t, "decode board", json.Unmarshal([]byte(f.run("pack_board", map[string]any{"detail_level": "compact"})), &board))
	if len(board.Workers) != 1 || board.Workers[0].Name != "parser-fix" || board.Workers[0].ShelveReason != reason {
		t.Fatalf("roster = %+v, want the shelved worker with its reason", board.Workers)
	}

	f.run("promote_overlay", map[string]any{"overlay_id": r1.ResultID})
	f.wantPrimary("fix.go", ready)
	f.wantChange("parser-fix", r1.ResultID, workerrecords.ChangeLanded)
	if rounds := f.worker("parser-fix").Rounds; len(rounds) != 1 {
		t.Fatalf("landing shelved changes ran a round: %d rounds", len(rounds))
	}
}

// Flow 10. A reviewer is bound to the exact result it was started on: it
// cannot change it, starting the review accounts for the result, and the
// implementer's later round does not move the review's subject.
func TestFlowReviewerReadsTheSealedResult(t *testing.T) {
	f := newWorkerFlow(t, "flow-review")
	const seed, v1, v2 = "package h\n", "package h\n\nvar Version = 1\n", "package h\n\nvar Version = 2\n"
	f.seed("handler.go", seed)

	f.scripts.push("implementer", writes("handler.go", v1), delivers("complete", "Wrote the handler."))
	f.start("impl", "implementer", "Write the handler.", "handler.go")
	f.drain()
	r1 := f.result("impl", 1)

	// The reviewer tries to edit its subject before it reports.
	f.scripts.push("code-reviewer", writes("handler.go", "package h\n\nvar Tampered = true\n"), delivers("complete", "Version should be 2."))
	f.run("task", map[string]any{
		"name": "reviewer", "agent_type": "code-reviewer", "review_of": r1.ResultID,
		"scope": map[string]any{"mode": "read"},
		"brief": map[string]any{"goal": "Review the handler.", "done_when": []any{"The handler is inspected."}},
	})
	if f.disposition("impl", r1.ResultID).AccountedAt == "" {
		t.Fatal("starting the review did not account for the result")
	}

	// The implementer is sent back while the review is still queued.
	f.scripts.push("implementer", writes("handler.go", v2), delivers("complete", "Set version 2."))
	f.run("instruct_worker", map[string]any{"worker": "impl", "instructions": "Set Version to 2."})
	f.drain()

	review := f.wantState("reviewer", workerstate.Delivered, workerstate.ReasonUnaccounted)
	if round := f.result("reviewer", 1); round.CheckpointID != "" || len(review.Rounds) != 1 {
		t.Fatalf("the reviewer's round changed files: %+v", round)
	}
	f.wantPrimary("handler.go", seed)

	// Both results still hold exactly what their rounds wrote.
	r2 := f.result("impl", 2)
	f.run("promote_overlay", map[string]any{"overlay_id": r2.ResultID})
	f.wantPrimary("handler.go", v2)
	f.wantChange("impl", r1.ResultID, workerrecords.ChangeLanded)
}

// Flow 12. A worker that answers without calling a tool is told to continue;
// at the bound the round fails with no report, and one instruction in the
// same conversation gets the report.
func TestFlowToolLessRoundFailsAndAnInstructionRecoversIt(t *testing.T) {
	f := newWorkerFlow(t, "flow-tool-less")
	f.seed("rec.go", "package rec\n")

	// No script: every response is prose.
	f.start("silent", "path-explorer", "Read rec.go.")
	f.drain()
	failed := f.wantState("silent", workerstate.Failed, workerstate.ReasonNoReport)
	if len(failed.Rounds) != 1 || failed.Rounds[0].SettledAt == "" {
		t.Fatalf("rounds = %+v, want one settled round", failed.Rounds)
	}

	f.scripts.push("path-explorer", reads("rec.go"), delivers("complete", "Read rec.go."))
	f.run("instruct_worker", map[string]any{"worker": "silent", "instructions": "Deliver your report."})
	f.drain()
	recovered := f.wantState("silent", workerstate.Delivered, workerstate.ReasonUnaccounted)
	if len(recovered.Rounds) != 2 || recovered.Worker.ChildSessionID != failed.Worker.ChildSessionID {
		t.Fatalf("recovered worker has %d rounds in session %s, want a second round in %s",
			len(recovered.Rounds), recovered.Worker.ChildSessionID, failed.Worker.ChildSessionID)
	}
}

// Flow 13. An idle surveyor that already read the code becomes the
// implementer with one instruction: same worker, same conversation, now with
// a write lane, and its changes land.
func TestFlowSurveyorIsReusedAsImplementer(t *testing.T) {
	f := newWorkerFlow(t, "flow-reuse")
	f.seed("token.go", "package auth\n\nvar Race = true\n")
	const fixed = "package auth\n\nvar Race = false\n"

	f.scripts.push("path-explorer", reads("token.go"), delivers("complete", "The refresh path races."))
	f.start("token", "path-explorer", "Survey the token refresh path.")
	f.drain()
	f.account("token refresh surveyed")
	surveyor := f.wantState("token", workerstate.Idle, workerstate.ReasonNone)
	if surveyor.Worker.AgentType != "path-explorer" || surveyor.Worker.ScopeMode != workerrecords.ScopeRead {
		t.Fatalf("surveyor = %+v", surveyor.Worker)
	}

	f.scripts.push("implementer", writes("token.go", fixed), delivers("complete", "Fixed the race."))
	f.run("instruct_worker", map[string]any{
		"worker": "token", "agent_type": "implementer",
		"scope":        map[string]any{"mode": "write", "paths": []any{"token.go"}},
		"brief":        "Fix the refresh race in token.go.",
		"instructions": "Apply the fix you identified.",
	})
	f.drain()
	implementer := f.wantState("token", workerstate.Delivered, workerstate.ReasonUnaccounted)
	if implementer.Worker.ID != surveyor.Worker.ID || implementer.Worker.ChildSessionID != surveyor.Worker.ChildSessionID ||
		implementer.Worker.AgentType != "implementer" || implementer.Worker.ScopeMode != workerrecords.ScopeWrite {
		t.Fatalf("reused worker = %+v, want the surveyor's worker and session as a write implementer", implementer.Worker)
	}
	if f.workerCount() != 1 {
		t.Fatalf("reuse created another worker: %d workers", f.workerCount())
	}
	f.run("promote_overlay", map[string]any{"overlay_id": f.result("token", 2).ResultID})
	f.wantPrimary("token.go", fixed)
}

// Flow 14. The coordinator makes a small edit itself; no worker exists.
func TestFlowSmallEditNeedsNoWorker(t *testing.T) {
	f := newWorkerFlow(t, "flow-small-edit")
	f.seed("direct.go", "package auth\n")
	const edited = "package auth\n\nvar Direct = 1\n"

	AttachDefaultAmbient(t, f.h, f.ctx, f.sess.ID)
	f.scripts.results["coordinator"] = "Set Direct in direct.go. [command#1]\n[verify: inspection - edit applied]"
	f.scripts.push("coordinator", writes("direct.go", edited),
		api.ToolCall{ID: "progress", Name: "update_progress", Args: f.h.ProgressUpdateArgs(t, f.ctx, f.sess.ID, "## Progress\n- [x] direct edit\n")})
	_, err := f.h.Sessions.Manager.Submissions.Prompt(f.ctx, f.sess.ID, "Set Direct to 1 in direct.go.")
	testutil.FailErr(t, "coordinator turn", err)

	f.wantPrimary("direct.go", edited)
	if n := f.workerCount(); n != 0 {
		t.Fatalf("a small direct edit created %d workers", n)
	}
}

// Flow 15, long wait. The worker delivers what it can; the coordinator
// shelves it and waits on a timer. The wait starts no round, and one
// instruction afterwards starts exactly one.
func TestFlowWaitingOutsideTheHostStartsNothingByItself(t *testing.T) {
	f := newWorkerFlow(t, "flow-outside-wait")
	f.seed("deploy.go", "package deploy\n")
	const staged = "package deploy\n\nvar Staged = true\n"

	f.scripts.push("implementer", writes("deploy.go", staged), delivers("partial", "Staged; the external check is still running."))
	f.start("deployer", "implementer", "Stage the deploy change.", "deploy.go")
	f.drain()
	r1 := f.result("deployer", 1)
	f.run("shelve_overlay", map[string]any{"overlay_id": r1.ResultID, "reason": "Waiting on the external check."})

	f.run("wait", map[string]any{"timeout_ms": float64(1000), "reason": "External check."})
	f.drain()
	if waited := f.wantState("deployer", workerstate.Idle, workerstate.ReasonNone); len(waited.Rounds) != 1 {
		t.Fatalf("the wait started a round: %d rounds", len(waited.Rounds))
	}

	f.scripts.push("implementer", delivers("complete", "The external check passed."))
	f.run("instruct_worker", map[string]any{"worker": "deployer", "instructions": "The check passed; finish."})
	f.drain()
	if finished := f.worker("deployer"); len(finished.Rounds) != 2 {
		t.Fatalf("one instruction started %d rounds, want 1 more", len(finished.Rounds)-1)
	}
	f.run("promote_overlay", map[string]any{"overlay_id": r1.ResultID})
	f.wantPrimary("deploy.go", staged)
}
