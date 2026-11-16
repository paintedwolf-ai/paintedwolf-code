package wiring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFlow09ShelvePersistAndLaterPromote(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow09-shelve-promote")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed fix.go", os.WriteFile(filepath.Join(dir, "fix.go"), []byte("package fix\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	scripts.push("implementer",
		api.ToolCall{ID: "f9-w", Name: "command", Args: map[string]any{"command": "printf 'package fix\\nvar Ready = true\\n' > fix.go"}},
		api.ToolCall{ID: "f9-d", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Prepared fix.", "objectives_met": []any{"Prepared"}}},
	)
	args := TaskToolArgs("implementer", "Prepare fix.go.", "fix.go")
	args["name"] = "parser-shelved"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start parser-shelved", err)
	testutil.FailErr(t, "drain parser-shelved", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds", err)
	r1 := rounds[0].ResultID

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "shelve_overlay", map[string]any{
		"overlay_id": r1,
		"reason":     "Hold until release window opens",
	}, call)
	testutil.FailErr(t, "shelve_overlay", err)

	call.Identity.ToolCallID = uuid.NewString()
	boardRaw, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": "compact"}, call)
	testutil.FailErr(t, "pack_board after shelve", err)
	var view api.BoardView
	testutil.FailErr(t, "decode board", json.Unmarshal([]byte(boardRaw), &view))
	if len(view.Workers) != 1 || !strings.Contains(view.Workers[0].ShelveReason, "Hold until release window opens") {
		t.Fatalf("roster missing shelved worker reason: %+v", view.Workers)
	}

	// Promoting the shelved result later lands directly without running another round.
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": r1}, call)
	testutil.FailErr(t, "promote shelved result", err)
	roundsAfter, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds after promote", err)
	if len(roundsAfter) != 1 {
		t.Fatalf("expected 1 round after promoting shelved result, got %d", len(roundsAfter))
	}
}

func TestFlow10SealedReviewBeforeLanding(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow10-sealed-review")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed handler.go", os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package h\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	scripts.push("implementer",
		api.ToolCall{ID: "f10-w1", Name: "command", Args: map[string]any{"command": "printf 'package h\\nvar Version = 1\\n' > handler.go"}},
		api.ToolCall{ID: "f10-d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Draft v1.", "objectives_met": []any{"Drafted v1"}}},
	)
	args := TaskToolArgs("implementer", "Write handler.go.", "handler.go")
	args["name"] = "impl"
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start impl", err)
	testutil.FailErr(t, "drain impl R1", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds R1", err)
	r1 := rounds[0].ResultID

	// Starting review_of R1 accounts for R1 and reads R1's sealed checkpoint.
	scripts.push("code-reviewer",
		api.ToolCall{ID: "rev-cat", Name: "command", Args: map[string]any{"command": "cat handler.go"}},
		api.ToolCall{ID: "rev-fin", Name: "complete_leg", Args: map[string]any{
			"leg_status":     "complete",
			"brief":          "Missing Version = 2 guard.",
			"objectives_met": []any{"Reviewed R1"},
			"findings":       []any{map[string]any{"path": "handler.go", "line": 2, "excerpt": "var Version = 1"}},
		}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{
		"name":       "reviewer-r1",
		"agent_type": "code-reviewer",
		"review_of":  r1,
		"scope":      map[string]any{"mode": "read"},
		"brief":      map[string]any{"goal": "Review R1", "done_when": []any{"Inspect handler.go"}},
	}, call)
	testutil.FailErr(t, "start reviewer of R1", err)
	testutil.FailErr(t, "drain reviewer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	scripts.push("implementer",
		api.ToolCall{ID: "f10-w2", Name: "command", Args: map[string]any{"command": "printf 'package h\\nvar Version = 2\\n' > handler.go"}},
		api.ToolCall{ID: "f10-d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Updated to Version = 2.", "objectives_met": []any{"Addressed review"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "impl", "instructions": "Set Version = 2 per reviewer-r1 finding."}, call)
	testutil.FailErr(t, "instruct impl R2", err)
	testutil.FailErr(t, "drain impl R2", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	rounds, err = h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds R2", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": rounds[1].ResultID}, call)
	testutil.FailErr(t, "promote R2", err)
}

func TestFlow12ToolLessRepairBoundAndOutcomeRedelivery(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow12-recovery")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed rec.go", os.WriteFile(filepath.Join(dir, "rec.go"), []byte("package rec\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	// Worker answers in prose with no tool calls, exhausting repair episode to report_missing.
	args := TaskToolArgs("path-explorer", "Read rec.go.")
	args["name"] = "silent-reader"
	args["scope"] = map[string]any{"mode": "read"}
	call.Identity.ToolCallID = uuid.NewString()
	_, err := h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start silent-reader", err)
	testutil.FailErr(t, "drain silent-reader failure", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	// Instructing asks for the report in the same conversation and succeeds.
	scripts.push("path-explorer",
		api.ToolCall{ID: "sr-ok", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Delivered report on retry.", "objectives_met": []any{"Reported"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "silent-reader", "instructions": "Deliver your report with complete_leg."}, call)
	testutil.FailErr(t, "instruct silent-reader", err)
	testutil.FailErr(t, "drain silent-reader recovery", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds", err)
	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds after report_missing + instruct, got %d", len(rounds))
	}
}

func TestFlow13Flow14AndFlow15ReuseSurveyorSmallEditAndExternalWait(t *testing.T) {
	h, scripts, sess, dir, actor := newFlowHarness(t, "flow13-14-15")
	ctx := h.OwnerCtx(t, t.Context())
	testutil.FailErr(t, "seed token.go", os.WriteFile(filepath.Join(dir, "token.go"), []byte("package auth\nvar Race = true\n"), 0o600))
	call := coordToolCtx(t, h, ctx, sess, dir)

	// Flow 14: direct two-line edit by the coordinator with zero workers in the session.
	testutil.FailErr(t, "seed direct.go", os.WriteFile(filepath.Join(dir, "direct.go"), []byte("package auth\nvar Direct = 0\n"), 0o600))
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	scripts.results["coordinator"] = "Updated direct.go directly. [command#1]\n[verify: inspection - Direct edit verified]"
	scripts.push("coordinator",
		api.ToolCall{ID: "c-edit", Name: "command", Args: map[string]any{"command": "printf 'package auth\\nvar Direct = 1\\n' > direct.go"}},
		api.ToolCall{ID: "c-prog", Name: "update_progress", Args: h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] wiring test plan\n")},
	)
	_, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Set Direct to 1 in direct.go.")
	testutil.FailErr(t, "direct coordinator edit", err)
	directBytes, err := os.ReadFile(filepath.Join(dir, "direct.go"))
	testutil.FailErr(t, "read direct.go", err)
	if !strings.Contains(string(directBytes), "Direct = 1") {
		t.Fatalf("direct.go = %q, want Direct = 1", string(directBytes))
	}
	ws0, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers after direct edit", err)
	if len(ws0) != 0 {
		t.Fatalf("expected 0 workers on small direct task, got %d", len(ws0))
	}

	// Flow 13: surveyor reads token.go, then instruct_worker upgrades it to implementer in the same session.
	scripts.push("path-explorer",
		api.ToolCall{ID: "sv-read", Name: "command", Args: map[string]any{"command": "cat token.go"}},
		api.ToolCall{ID: "sv-done", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Found refresh race in token.go.", "objectives_met": []any{"Surveyed"}}},
	)
	sArgs := TaskToolArgs("path-explorer", "Survey token refresh.")
	sArgs["name"] = "token-worker"
	sArgs["scope"] = map[string]any{"mode": "read"}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", sArgs, call)
	testutil.FailErr(t, "start surveyor", err)
	testutil.FailErr(t, "drain surveyor", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	surveyChild := ws[0].ChildSessionID
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] surveyed\n"), call)
	testutil.FailErr(t, "account survey", err)

	scripts.push("implementer",
		api.ToolCall{ID: "im-edit", Name: "command", Args: map[string]any{"command": "printf 'package auth\\nvar Race = false\\n' > token.go"}},
		api.ToolCall{ID: "im-done", Name: "complete_leg", Args: map[string]any{"leg_status": "partial", "brief": "Staged fix; waiting on external staging check.", "objectives_met": []any{"Staged"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker":       "token-worker",
		"agent_type":   "implementer",
		"brief":        "Fix the refresh race in token.go.",
		"instructions": "Apply the race fix now.",
	}, call)
	testutil.FailErr(t, "upgrade surveyor to implementer", err)
	testutil.FailErr(t, "drain upgraded implementer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	reloaded, err := h.Delegations.Records.Worker(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "reload upgraded worker", err)
	if reloaded.ChildSessionID != surveyChild || reloaded.AgentType != "implementer" || reloaded.ScopeMode != workerrecords.ScopeWrite {
		t.Fatalf("upgraded worker mismatch: %+v (want child=%s)", reloaded, surveyChild)
	}

	// Flow 15 (long wait): shelve while waiting on external check, park on timer wait, then instruct_worker.
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds after R2", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "shelve_overlay", map[string]any{"overlay_id": rounds[1].ResultID, "reason": "Waiting on external staging check"}, call)
	testutil.FailErr(t, "shelve for external check", err)

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "wait", map[string]any{"timeout_ms": float64(1000), "reason": "External staging check"}, call)
	testutil.FailErr(t, "timer wait", err)
	roundsDuringWait, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds during timer wait", err)
	if len(roundsDuringWait) != 2 {
		t.Fatalf("timer wait started unexpected round: %d", len(roundsDuringWait))
	}

	scripts.push("implementer",
		api.ToolCall{ID: "im-final", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "External check passed; ready to land.", "objectives_met": []any{"Verified"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "token-worker", "instructions": "External check passed; finalize."}, call)
	testutil.FailErr(t, "instruct after external wait", err)
	testutil.FailErr(t, "drain R3", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	roundsFinal, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds final", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": roundsFinal[2].ResultID}, call)
	testutil.FailErr(t, "promote final token.go", err)
	landed, err := os.ReadFile(filepath.Join(dir, "token.go"))
	testutil.FailErr(t, "read landed token.go", err)
	if !strings.Contains(string(landed), "Race = false") {
		t.Fatalf("landed token.go = %q", string(landed))
	}
}
