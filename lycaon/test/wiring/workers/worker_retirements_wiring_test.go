package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/test/wiring"

	"github.com/google/uuid"
	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetirementsAbsentFromActiveSurfacesAndMapped(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "retirements-surface")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	call := h.CoordinatorToolContext(t, ctx, sess, dir)

	retiredTools := []string{
		"request_budget",
		"extend_worker_budget",
		"decline_worker_budget",
		"delegate_init",
		"delegate_decompose",
		"delegate_dispatch",
		"delegate_status",
		"parse_decomposition",
		"parse_delegation_plan",
	}
	retirementsPath := filepath.Join("..", "..", "..", "schemas", "agent-public-retirements.json")
	rawLedger, err := os.ReadFile(retirementsPath)
	testutil.FailErr(t, "read agent-public-retirements.json", err)
	var ledger struct {
		Vocabularies struct {
			Tools map[string]any `json:"tools"`
		} `json:"vocabularies"`
	}
	testutil.FailErr(t, "decode retirement ledger", json.Unmarshal(rawLedger, &ledger))

	for _, name := range retiredTools {
		if _, ok := h.ToolRegistry.Definition(name); ok {
			t.Fatalf("retired tool %q remains registered", name)
		}
		call.Identity.ToolCallID = uuid.NewString()
		if _, runErr := h.ToolRegistry.Run(ctx, name, map[string]any{}, call); runErr == nil {
			t.Fatalf("retired tool %q succeeded on Run", name)
		}
		if _, mapped := ledger.Vocabularies.Tools[name]; !mapped {
			t.Fatalf("retired tool %q missing from agent-public-retirements.json", name)
		}
	}

	assertSchemaOmitsProps(t, h, "task", "max_tool_loops", "child_session_id")
	assertSchemaOmitsProps(t, h, "pack_board", "finding_id", "findings_after", "review_view", "assignment_id")

	for _, route := range []string{"/v1/delegations", "/v1/delegations/del-1"} {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		req.Header.Set("Authorization", "Bearer "+internalapi.TestAPIToken)
		rec := httptest.NewRecorder()
		h.Server.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s status = %d, want 404 or 405", route, rec.Code)
		}
	}

	var count int
	testutil.FailErr(t, "query retired delegation tables", h.DB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN ('delegations', 'delegation_legs')",
	).Scan(&count))
	if count != 0 {
		t.Fatalf("delegation table count = %d, want 0", count)
	}
}

func assertSchemaOmitsProps(t *testing.T, h *wiring.Harness, toolName string, forbidden ...string) {
	t.Helper()
	def, ok := h.ToolRegistry.Definition(toolName)
	if !ok {
		t.Fatalf("tool %q missing from registry", toolName)
	}
	props, _ := def.Meta.ArgsSchema["properties"].(map[string]any)
	for _, prop := range forbidden {
		if _, exists := props[prop]; exists {
			t.Fatalf("tool %q schema still exposes retired property %q", toolName, prop)
		}
	}
}

func TestRetirementsReplacementRoutesAndKeptTools(t *testing.T) {
	var h *wiring.Harness
	scripts := wiring.NewQueuedWorkerScripts()
	h = wiring.BuildForTest(t, wiring.WithLLMClient(scripts.Mock(&h)), wiring.WithoutCoordinatorLoop())
	off := false
	testutil.FailErr(t, "disable scanners", h.Boards.Snapshot.SecurityScanners.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "retirements-replacements")
	wiring.WriteTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed mod.go", os.WriteFile(filepath.Join(dir, "mod.go"), []byte("package mod\n"), 0o600))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)
	call := h.CoordinatorToolContext(t, ctx, sess, dir)
	actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}

	scripts.Push("implementer",
		api.ToolCall{ID: "m1", Name: "message_coordinator", Args: map[string]any{
			"text":        "Scope touches validation too.",
			"needs_reply": true,
		}},
		api.ToolCall{ID: "f1", Name: "record_finding", Args: map[string]any{
			"summary": "Input parser skips empty lines",
			"ref":     "mod.go",
		}},
		api.ToolCall{ID: "q1", Name: "request_decision", Args: map[string]any{
			"question":      "How should empty input be handled?",
			"blocker_class": "decision",
		}},
		api.ToolCall{ID: "w1", Name: "command", Args: map[string]any{"command": "printf 'package mod\\nvar V = 1\\n' > mod.go"}},
		api.ToolCall{ID: "d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Round 1 edit.", "objectives_met": []any{"Edited V=1"}}},
	)
	args := wiring.TaskToolArgs("implementer", "Fix mod.go.", "mod.go")
	args["name"] = "mod-fixer"
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", args, call)
	testutil.FailErr(t, "start mod-fixer", err)
	testutil.FailErr(t, "drain to open question", wiring.DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	_, _, err = h.Sessions.Manager.Coordinator.Assembly.WorkerRoster(ctx, sess.ID)
	testutil.FailErr(t, "render roster before answer", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "answer_decision", map[string]any{
		"worker": "mod-fixer",
		"answer": "Return nil slice on empty input without error.",
	}, call)
	testutil.FailErr(t, "free-text answer_decision", err)
	testutil.FailErr(t, "drain round 1", wiring.DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	verifyFindingsAndHistoryReplacements(t, h, ctx, call)
	verifyOverlayHandlesAndWithdraw(t, h, ctx, sess, dir, call, actor, scripts)
}

func verifyFindingsAndHistoryReplacements(t *testing.T, h *wiring.Harness, ctx context.Context, call tools.ToolContext) {
	t.Helper()
	call.Identity.ToolCallID = uuid.NewString()
	pagedRaw, err := h.ToolRegistry.Run(ctx, "read_findings", map[string]any{"findings_after": float64(0)}, call)
	testutil.FailErr(t, "read_findings page", err)
	if !strings.Contains(pagedRaw, "Input parser skips empty lines") {
		t.Fatalf("read_findings page missing finding summary: %s", pagedRaw)
	}
	call.Identity.ToolCallID = uuid.NewString()
	byIDRaw, err := h.ToolRegistry.Run(ctx, "read_findings", map[string]any{"finding_id": float64(1)}, call)
	testutil.FailErr(t, "read_findings by id", err)
	if !strings.Contains(byIDRaw, "mod.go") {
		t.Fatalf("read_findings by id missing ref: %s", byIDRaw)
	}

	call.Identity.ToolCallID = uuid.NewString()
	histRaw, err := h.ToolRegistry.Run(ctx, "worker_history", map[string]any{"worker": "mod-fixer"}, call)
	testutil.FailErr(t, "worker_history", err)
	if !strings.Contains(histRaw, "Scope touches validation too.") {
		t.Fatalf("worker_history missing worker message: %s", histRaw)
	}

	for _, level := range []string{"compact", "forensic"} {
		call.Identity.ToolCallID = uuid.NewString()
		boardRaw, bErr := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": level}, call)
		testutil.FailErr(t, "pack_board "+level, bErr)
		var view api.BoardView
		testutil.FailErr(t, "decode board "+level, json.Unmarshal([]byte(boardRaw), &view))
		if len(view.Workers) != 1 || view.Workers[0].Name != "mod-fixer" {
			t.Fatalf("board(%s) workers = %+v, want single mod-fixer row", level, view.Workers)
		}
		if level == "forensic" && (view.ForensicWorkers == nil || len(view.ForensicWorkers.Tasks) == 0) {
			t.Fatalf("forensic pack_board omitted worker tasks: %+v", view.ForensicWorkers)
		}
	}
}

func verifyOverlayHandlesAndWithdraw(
	t *testing.T,
	h *wiring.Harness,
	ctx context.Context,
	sess *api.Session,
	dir string,
	call tools.ToolContext,
	actor workerrecords.Actor,
	scripts *wiring.QueuedWorkerScripts,
) {
	t.Helper()
	ws, err := h.Workers.Records.Workers(ctx, actor)
	testutil.FailErr(t, "list workers", err)
	rounds, err := h.Workers.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "list rounds R1", err)
	r1 := rounds[0].ResultID

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "preview_overlay", map[string]any{"overlay_id": r1}, call)
	testutil.FailErr(t, "preview_overlay R1", err)

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "shelve_overlay", map[string]any{"overlay_id": r1, "reason": "Hold while checking round 2"}, call)
	testutil.FailErr(t, "shelve_overlay R1", err)

	scripts.Push("implementer",
		api.ToolCall{ID: "w2", Name: "command", Args: map[string]any{"command": "printf 'package mod\\nvar V = 999\\n' > mod.go"}},
		api.ToolCall{ID: "d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Round 2 experiment.", "objectives_met": []any{"Tried V=999"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": "mod-fixer", "instructions": "Try V=999."}, call)
	testutil.FailErr(t, "instruct_worker R2", err)
	testutil.FailErr(t, "drain R2", wiring.DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	rounds, err = h.Workers.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "list rounds R2", err)
	r2 := rounds[1].ResultID

	// Rejecting R2 drops only R2 and restores R1's checkpoint for promotion.
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "reject_overlay", map[string]any{"overlay_id": r2, "reason": "Drop experiment"}, call)
	testutil.FailErr(t, "reject_overlay R2", err)

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": r1}, call)
	testutil.FailErr(t, "promote_overlay R1", err)
	landed, err := os.ReadFile(filepath.Join(dir, "mod.go"))
	testutil.FailErr(t, "read landed mod.go", err)
	if !strings.Contains(string(landed), "V = 1") {
		t.Fatalf("landed mod.go = %q, want R1 content V = 1", string(landed))
	}

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] mod-fixer landed\n"), call)
	testutil.FailErr(t, "update_progress", err)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = h.DB.ExecContext(
		ctx,
		`INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		 VALUES (?, ?, ?, 'vet', '1.0.0', 'running', 'work', ?, ?)`,
		"run-stop-check", sess.ID, sess.ProjectID, now, now,
	)
	testutil.FailErr(t, "seed workflow run for stop check", err)
	planned, err := h.Workers.Records.CreateWorker(ctx, actor, workerrecords.Op{ID: "seed-run-stop"}, workerrecords.WorkerSpec{
		Name: "planned-stop-leg", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite,
		WorkflowRunID: "run-stop-check", WorkflowPhase: "work", WorkflowWorkID: "leg-stop",
	})
	testutil.FailErr(t, "seed planned leg", err)
	testutil.FailErr(t, "withdraw planned workers on run stop", h.Workflows.Manager.Controls.Cleanup.Workers.WithdrawPlannedWorkersByRunID(ctx, "run-stop-check"))
	reloaded, err := h.Workers.Records.Worker(ctx, actor, planned.WorkerID)
	testutil.FailErr(t, "reload withdrawn planned leg", err)
	if reloaded.ClosedAt == "" || workerstate.Derive(workerstate.Facts{Closed: reloaded.ClosedAt != "", Planned: true}).State != workerstate.Closed {
		t.Fatalf("expected planned leg withdrawn and closed on run stop, got %+v", reloaded)
	}
}

func TestRetirementsHistoricalTranscriptReadback(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "retirements-history")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)

	now := time.Now().UTC()
	testutil.FailErr(t, "append historical retired tool calls", h.Store.AppendMessages(ctx, sess.ID,
		api.Message{
			ID:      uuid.NewString(),
			Role:    api.MessageRoleAssistant,
			Content: "Dispatching planned leg.",
			ToolCalls: []api.ToolCall{
				{ID: "hist-1", Name: "delegate_dispatch", Args: map[string]any{"leg_id": "leg-1"}},
				{ID: "hist-2", Name: "request_budget", Args: map[string]any{"requested_max": 12}},
			},
			CreatedAt: now,
		},
		api.Message{
			ID:      uuid.NewString(),
			Role:    api.MessageRoleTool,
			Content: `{"job_id":"job-hist-1","status":"queued"}`,
			ToolResult: &api.ToolResult{
				Tool:       "delegate_dispatch",
				ToolCallID: "hist-1",
				Content:    `{"job_id":"job-hist-1","status":"queued"}`,
			},
			CreatedAt: now.Add(time.Second),
		},
	))

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages with retired tool history", err)
	if len(msgs) < 2 || len(msgs[0].ToolCalls) != 2 || msgs[0].ToolCalls[0].Name != "delegate_dispatch" {
		t.Fatalf("historical tool calls did not round-trip: %+v", msgs)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+internalapi.TestAPIToken)
	rec := httptest.NewRecorder()
	h.Server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "delegate_dispatch") || !strings.Contains(rec.Body.String(), "request_budget") {
		t.Fatalf("GET messages status=%d body=%s", rec.Code, rec.Body.String())
	}

	evRec := evidence.BuildEvidenceRecord(dir, "delegate_dispatch", map[string]any{"leg_id": "leg-1"}, `{"job_id":"job-hist-1"}`)
	if evRec.Kind != "" {
		t.Fatalf("retired non-observation tool unexpectedly minted evidence kind %q", evRec.Kind)
	}
	for _, repl := range []string{"instruct_worker", "shelve_overlay"} {
		replRec := evidence.BuildEvidenceRecord(dir, repl, map[string]any{}, `{"status":"ok"}`)
		if replRec.Kind == "" || replRec.Fidelity != evidence.FidelityStructured {
			t.Fatalf("replacement tool %q failed to build structured evidence record: %+v", repl, replRec)
		}
	}
	_, err = h.Store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger with retired tool history", err)
}
