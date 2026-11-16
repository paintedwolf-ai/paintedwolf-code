package wiring

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func newFlowHarness(t *testing.T, name string) (*Harness, *queuedWorkerScripts, *api.Session, string, workerrecords.Actor) {
	t.Helper()
	var h *Harness
	scripts := newQueuedWorkerScripts()
	h = BuildForTest(t, WithLLMClient(scripts.mock(&h)), WithoutCoordinatorLoop())
	off := false
	testutil.FailErr(t, "disable scanners", h.Boards.Snapshot.SecurityScanners.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, name)
	writeTestProjectApprovalsAllowWrite(t, dir)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)
	actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}
	return h, scripts, sess, dir, actor
}

func TestFlow01ReadFanOutSixWorkers(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow01-read-fanout")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed auth.go", os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package auth\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	for i := 1; i <= 6; i++ {
		scripts.push("path-explorer",
			api.ToolCall{ID: fmt.Sprintf("r-%d", i), Name: "command", Args: map[string]any{"command": "cat auth.go"}},
			api.ToolCall{ID: fmt.Sprintf("d-%d", i), Name: "complete_leg", Args: map[string]any{
				"leg_status": "complete", "brief": fmt.Sprintf("Subsystem %d surveyed.", i), "objectives_met": []any{"Surveyed"},
			}},
		)
		args := TaskToolArgs("path-explorer", fmt.Sprintf("Survey auth subsystem %d.", i))
		args["name"] = fmt.Sprintf("auth-%d", i)
		args["scope"] = map[string]any{"mode": "read"}
		call.Identity.ToolCallID = uuid.NewString()
		_, err := h.ToolRegistry.Run(ctx, "task", args, call)
		testutil.FailErr(t, "dispatch read worker", err)
	}

	call.Identity.ToolCallID = uuid.NewString()
	waitOut, err := h.ToolRegistry.Run(ctx, "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "all_workers_idle"}},
	}, call)
	testutil.FailErr(t, "wait all_workers_idle", err)
	if !strings.Contains(waitOut, "parked") && !strings.Contains(waitOut, "all_workers_idle") {
		t.Fatalf("unexpected wait response: %s", waitOut)
	}

	testutil.FailErr(t, "drain 6 read workers", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID))
	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "list workers", err)
	if len(ws) != 6 {
		t.Fatalf("workers = %d, want 6", len(ws))
	}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] all six surveyed\n"), call)
	testutil.FailErr(t, "update_progress for all six", err)
}

func TestFlow02OneWriteWorkerCorrectedOnce(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow02-write-corrected")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed parser.go", os.WriteFile(filepath.Join(dir, "parser.go"), []byte("package parser\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	scripts.push("implementer",
		api.ToolCall{ID: "p1", Name: "command", Args: map[string]any{"command": "printf 'package parser\\nfunc Parse(s string) bool { return len(s) > 0 }\\n' > parser.go"}},
		api.ToolCall{ID: "d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Fixed parser.", "objectives_met": []any{"Fixed"}}},
	)
	args := TaskToolArgs("implementer", "Fix parser failure.", "parser.go")
	args["name"] = "parser-fix"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start parser-fix", err)
	testutil.FailErr(t, "drain R1", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	r1Child := ws[0].ChildSessionID

	scripts.push("implementer",
		api.ToolCall{ID: "p2", Name: "command", Args: map[string]any{"command": "printf 'package parser\\nfunc Parse(s string) bool { return s != \"\" }\\n// empty-input covered\\n' > parser.go"}},
		api.ToolCall{ID: "d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Added empty-input coverage.", "objectives_met": []any{"Added coverage"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "parser-fix", "instructions": "Add empty-input coverage."}, call)
	testutil.FailErr(t, "instruct parser-fix", err)
	testutil.FailErr(t, "drain R2", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	reloaded, err := h.Delegations.Records.Worker(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "reload parser-fix", err)
	if reloaded.ChildSessionID != r1Child {
		t.Fatalf("child session changed: %q != %q", reloaded.ChildSessionID, r1Child)
	}
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": rounds[1].ResultID}, call)
	testutil.FailErr(t, "promote R2", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] parser-fix landed\n"), call)
	testutil.FailErr(t, "update_progress", err)
}

func TestFlow03ThreeWriteWorkersLandingOneConflict(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow03-three-write-conflict")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed shared.go", os.WriteFile(filepath.Join(dir, "shared.go"), []byte("package app\n// base\n"), 0o600))
	testutil.FailErr(t, "seed extra.go", os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package app\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	specs := []struct{ name, file, body string }{
		{"worker-a", "shared.go", "package app\nvar A = 1\n"},
		{"worker-b", "shared.go", "package app\nvar B = 2\n"},
		{"worker-c", "extra.go", "package app\nvar C = 3\n"},
	}
	for _, sp := range specs {
		scripts.push("implementer",
			api.ToolCall{ID: sp.name + "-w", Name: "command", Args: map[string]any{"command": fmt.Sprintf("printf %q > %s", sp.body, sp.file)}},
			api.ToolCall{ID: sp.name + "-d", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": sp.name, "objectives_met": []any{"Done"}}},
		)
		args := TaskToolArgs("implementer", "Edit "+sp.file, sp.file)
		args["name"] = sp.name
		call.Identity.ToolCallID = uuid.NewString()
		_, err := h.ToolRegistry.Run(ctx, "task", args, call)
		testutil.FailErr(t, "start "+sp.name, err)
	}
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "wait", map[string]any{"conditions": []any{map[string]any{"kind": "next_worker_done"}}}, call)
	testutil.FailErr(t, "wait next_worker_done", err)
	testutil.FailErr(t, "drain all three", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	byName := map[string]string{}
	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	for _, w := range ws {
		rds, rErr := h.Delegations.Records.Rounds(ctx, actor, w.ID)
		testutil.FailErr(t, "rounds "+w.Name, rErr)
		byName[w.Name] = rds[0].ResultID
	}

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": byName["worker-a"]}, call)
	testutil.FailErr(t, "promote worker-a", err)

	call.Identity.ToolCallID = uuid.NewString()
	previewRaw, err := h.ToolRegistry.Run(ctx, "preview_overlay", map[string]any{"overlay_id": byName["worker-b"]}, call)
	testutil.FailErr(t, "preview worker-b", err)
	if !strings.Contains(previewRaw, "shared.go") {
		t.Fatalf("preview missing shared.go conflict: %s", previewRaw)
	}

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{
		"overlay_id":  byName["worker-b"],
		"resolutions": []any{map[string]any{"path": "shared.go", "content": "package app\nvar A = 1\nvar B = 2\n"}},
	}, call)
	testutil.FailErr(t, "promote worker-b with resolution", err)

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": byName["worker-c"]}, call)
	testutil.FailErr(t, "promote worker-c", err)
}

func TestFlow04SandboxQuestionRelayAndCancelWithdraw(t *testing.T) {
	h, scripts, sess, dir, _ := newFlowHarness(t, "flow04-sandbox-question")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	scripts.push("implementer",
		api.ToolCall{ID: "sq-1", Name: "request_decision", Args: map[string]any{
			"question": "Need write access to /etc/hosts fixture.", "blocker_class": "sandbox",
		}},
	)
	args := TaskToolArgs("implementer", "Configure host fixture.", "main.go")
	args["name"] = "sandbox-asker"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start sandbox-asker", err)
	testutil.FailErr(t, "drain to asking", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "sandbox-asker", "instructions": "Proceed anyway."}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "INSTRUCT_WORKER_ASKING" {
		t.Fatalf("expected INSTRUCT_WORKER_ASKING while asking, got %v", err)
	}

	scripts.push("implementer",
		api.ToolCall{ID: "sq-done", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Used project-local fixture.", "objectives_met": []any{"Done"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "answer_decision", map[string]any{
		"worker": "sandbox-asker", "answer": "Use a project-local hosts file instead.", "resolved_by": "user",
	}, call)
	testutil.FailErr(t, "answer_decision resolved_by user", err)
	testutil.FailErr(t, "drain resumed sandbox-asker", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	scripts.push("implementer",
		api.ToolCall{ID: "cq-1", Name: "request_decision", Args: map[string]any{"question": "Cancelable question?", "blocker_class": "decision"}},
	)
	args2 := TaskToolArgs("implementer", "Second worker.", "main.go")
	args2["name"] = "cancel-asker"
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", args2, call)
	testutil.FailErr(t, "start cancel-asker", err)
	testutil.FailErr(t, "drain cancel-asker to asking", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "worker_cancel", map[string]any{"worker": "cancel-asker", "reason": "Withdrawn"}, call)
	testutil.FailErr(t, "worker_cancel asking worker", err)
}

func TestFlow05Flow06AndFlow08WrongDirectionCancelAndPrerequisites(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow05-06-08")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed a.go", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o600))
	testutil.FailErr(t, "seed b.go", os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	// Flow 5 + Flow 8: producer schema edits both a.go and b.go; consumer waits via after_workers.
	scripts.push("implementer",
		api.ToolCall{ID: "s-w1", Name: "command", Args: map[string]any{"command": "printf 'package a\\nvar Bad = true\\n' > a.go"}},
		api.ToolCall{ID: "s-d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Wrong rewrite.", "objectives_met": []any{"Rewrote"}}},
	)
	sArgs := TaskToolArgs("implementer", "Update schema.", "a.go", "b.go")
	sArgs["name"] = "schema"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", sArgs, call)
	testutil.FailErr(t, "start schema", err)

	cArgs := TaskToolArgs("implementer", "Update caller after schema.", "b.go")
	cArgs["name"] = "caller"
	cArgs["after_workers"] = []any{"schema"}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", cArgs, call)
	testutil.FailErr(t, "start waiting caller", err)

	testutil.FailErr(t, "drain schema R1", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	var schemaW, callerW workerrecords.Worker
	for _, w := range ws {
		switch w.Name {
		case "schema":
			schemaW = w
		case "caller":
			callerW = w
		}
	}
	callerRounds, err := h.Delegations.Records.Rounds(ctx, actor, callerW.ID)
	testutil.FailErr(t, "caller rounds before promote", err)
	if len(callerRounds) != 1 || callerRounds[0].ResultID != "" {
		t.Fatalf("caller should still wait before schema lands: %+v", callerRounds)
	}

	// Flow 5 Begin again: reject R1, then instruct schema from clean source; promote with drop.
	schemaRounds, err := h.Delegations.Records.Rounds(ctx, actor, schemaW.ID)
	testutil.FailErr(t, "schema rounds R1", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "reject_overlay", map[string]any{"overlay_id": schemaRounds[0].ResultID, "reason": "Too broad"}, call)
	testutil.FailErr(t, "reject R1", err)

	scripts.push("implementer",
		api.ToolCall{ID: "s-w2", Name: "command", Args: map[string]any{"command": "printf 'package a\\nvar Schema = 2\\n' > a.go && printf 'package a\\nvar DropMe = 1\\n' > scratch.go"}},
		api.ToolCall{ID: "s-d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Two-line fix.", "objectives_met": []any{"Fixed"}}},
	)
	scripts.push("implementer",
		api.ToolCall{ID: "c-w1", Name: "command", Args: map[string]any{"command": "printf 'package a\\nvar Caller = Schema\\n' > b.go"}},
		api.ToolCall{ID: "c-d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Caller updated.", "objectives_met": []any{"Updated caller"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "schema", "instructions": "Apply the two-line fix."}, call)
	testutil.FailErr(t, "instruct schema R2", err)
	testutil.FailErr(t, "drain schema R2", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	schemaRounds, err = h.Delegations.Records.Rounds(ctx, actor, schemaW.ID)
	testutil.FailErr(t, "schema rounds R2", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{
		"overlay_id": schemaRounds[1].ResultID,
		"drop":       []any{"scratch.go"},
	}, call)
	testutil.FailErr(t, "promote schema R2 with drop", err)
	if _, statErr := os.Stat(filepath.Join(dir, "scratch.go")); !os.IsNotExist(statErr) {
		t.Fatalf("dropped scratch.go landed on disk: %v", statErr)
	}

	// Promoting schema releases caller's prerequisite so caller now runs and delivers.
	testutil.FailErr(t, "drain released caller", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
	callerRounds, err = h.Delegations.Records.Rounds(ctx, actor, callerW.ID)
	testutil.FailErr(t, "caller rounds after promote", err)
	if len(callerRounds) != 1 || callerRounds[0].ResultID == "" {
		t.Fatalf("caller did not settle after schema landed: %+v", callerRounds)
	}

	// Flow 6: cancel a running worker after a partial turn, then redirect with instruct_worker.
	scripts.push("implementer",
		api.ToolCall{ID: "u-q", Name: "request_decision", Args: map[string]any{"question": "Long step checkpoint?", "blocker_class": "decision"}},
	)
	uArgs := TaskToolArgs("implementer", "Long run.", "a.go")
	uArgs["name"] = "urgent"
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", uArgs, call)
	testutil.FailErr(t, "start urgent", err)
	testutil.FailErr(t, "drain urgent to checkpoint", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "worker_cancel", map[string]any{"worker": "urgent", "reason": "Wrong approach"}, call)
	testutil.FailErr(t, "cancel urgent", err)

	scripts.push("implementer",
		api.ToolCall{ID: "u-d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Redirected cleanly.", "objectives_met": []any{"Redirected"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "urgent", "instructions": "Take the fast path instead."}, call)
	testutil.FailErr(t, "instruct redirected urgent", err)
	testutil.FailErr(t, "drain redirected urgent", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
}

func TestFlow07WorkerHistoryAndKeptWorkOnFailedRound(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow07-history-kept-work")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed long.go", os.WriteFile(filepath.Join(dir, "long.go"), []byte("package long\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	// Worker edits long.go then returns prose without calling complete_leg across the repair bound.
	scripts.push("implementer",
		api.ToolCall{ID: "l-w1", Name: "command", Args: map[string]any{"command": "printf 'package long\\nvar Kept = true\\n' > long.go"}},
	)
	args := TaskToolArgs("implementer", "Long edit.", "long.go")
	args["name"] = "long-runner"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start long-runner", err)
	testutil.FailErr(t, "drain failed round", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	call.Identity.ToolCallID = uuid.NewString()
	histRaw, err := h.ToolRegistry.Run(ctx, "worker_history", map[string]any{"worker": "long-runner"}, call)
	testutil.FailErr(t, "worker_history long-runner", err)
	if !strings.Contains(histRaw, "long-runner") {
		t.Fatalf("worker_history missing long-runner: %s", histRaw)
	}

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds", err)
	if len(rounds) != 1 || rounds[0].ResultID == "" {
		t.Fatalf("expected failed round to retain result handle, got %+v", rounds)
	}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "preview_overlay", map[string]any{"overlay_id": rounds[0].ResultID}, call)
	testutil.FailErr(t, "preview kept work on failed round", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "shelve_overlay", map[string]any{"overlay_id": rounds[0].ResultID, "reason": "Hold kept work from failed round"}, call)
	testutil.FailErr(t, "shelve kept work on failed round", err)
}
