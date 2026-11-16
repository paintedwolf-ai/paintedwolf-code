package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type queuedWorkerScripts struct {
	mu      sync.Mutex
	queues  map[string][][]api.ToolCall
	active  map[string][]api.ToolCall
	step    map[string]int
	results map[string]string
}

func newQueuedWorkerScripts() *queuedWorkerScripts {
	return &queuedWorkerScripts{
		queues:  map[string][][]api.ToolCall{},
		active:  map[string][]api.ToolCall{},
		step:    map[string]int{},
		results: map[string]string{},
	}
}

func (q *queuedWorkerScripts) push(agentType string, script ...api.ToolCall) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queues[agentType] = append(q.queues[agentType], script)
}

func (q *queuedWorkerScripts) mock(h **Harness) *llm.KickDrivenMock {
	return llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llm.Snapshot) modelcall.Completion {
			jobID := workercontext.Job(ctx)
			if jobID == "" {
				return modelcall.Completion{Content: "All obligations are complete."}
			}
			task, ok := (*h).Delegations.Queue.Get(jobID)
			if !ok || task == nil {
				return modelcall.Completion{Content: "Done."}
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			idx, hasStarted := q.step[jobID]
			if !hasStarted {
				q.step[jobID] = 0
				if list := q.queues[task.AgentType]; len(list) > 0 {
					q.active[jobID] = list[0]
					q.queues[task.AgentType] = list[1:]
				}
			}
			script := q.active[jobID]
			if idx >= len(script) {
				return modelcall.Completion{Content: "Done."}
			}
			call := script[idx]
			q.step[jobID] = idx + 1
			return modelcall.Completion{ToolCalls: []api.ToolCall{call}}
		},
	})
}

func coordToolCtx(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, dir string) tools.ToolContext {
	t.Helper()
	call := wiringToolContext(sess.ID, dir)
	call.Identity.ProjectID = sess.ProjectID
	call.Identity.Agent = "coordinator"
	testutil.FailErr(t, "resolve primary root", h.DB.QueryRowContext(
		ctx,
		"SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1",
		sess.ProjectID,
	).Scan(&call.Source.ActiveRootID))
	call.Source.Roots[0].ID = call.Source.ActiveRootID
	return call
}

func approveBlueprintOnDisk(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, dir, relPath, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(relPath))
	testutil.FailErr(t, "mkdir blueprint dir", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write blueprint", os.WriteFile(abs, []byte(content), 0o600))
	now := db.FormatTime(time.Now().UTC())
	_, err := h.DB.ExecContext(ctx, `
		INSERT INTO blueprint_approvals (
			project_id, path, content_digest, workflow_revision, status,
			approved_at, approved_via, approved_by_person_id, session_id
		) VALUES (?, ?, ?, 0, 'approved', ?, 'chat', (SELECT id FROM people WHERE role = 'owner'), ?)
		ON CONFLICT (project_id, path) DO UPDATE SET
			content_digest = excluded.content_digest,
			status = 'approved',
			approved_at = excluded.approved_at,
			revoked_at = NULL,
			revoked_cause = ''`,
		sess.ProjectID, relPath, blueprint.ContentDigest(content), now, sess.ID)
	testutil.FailErr(t, "upsert blueprint approval", err)
}

func TestBlueprintFlow11CriteriaReconcileAndBoardPlanFilter(t *testing.T) {
	var h *Harness
	scripts := newQueuedWorkerScripts()
	h = BuildForTest(t, WithLLMClient(scripts.mock(&h)), WithoutCoordinatorLoop())
	scannersOff := false
	testutil.FailErr(t, "disable auto-scanners", h.Boards.Snapshot.SecurityScanners.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &scannersOff}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "wc07-flow11")
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed app.go", os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o600))
	testutil.FailErr(t, "seed ext.go", os.WriteFile(filepath.Join(dir, "ext.go"), []byte("package app\n"), 0o600))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	call := coordToolCtx(t, h, ctx, sess, dir)
	actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}

	bpPath := blueprint.ConventionPath("wc07-flow11.md")
	bpV1 := "# Plan\n\n<!-- lycaon:tasks [\n" +
		`  {"id":"leg-core","title":"Core edit","files":["app.go"],"verify":["echo wc07-ok"]},` + "\n" +
		`  {"id":"leg-ext","title":"Ext edit","files":["ext.go"],"depends_on":["leg-core"]}` + "\n" +
		"] -->\n"
	absBP := filepath.Join(dir, filepath.FromSlash(bpPath))
	testutil.FailErr(t, "mkdir draft bp", os.MkdirAll(filepath.Dir(absBP), 0o755))
	testutil.FailErr(t, "write draft bp", os.WriteFile(absBP, []byte(bpV1), 0o600))

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "plan_from_blueprint", map[string]any{"blueprint_path": bpPath}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "plan_not_approved" {
		t.Fatalf("expected plan_not_approved on draft blueprint, got %v", err)
	}

	approveBlueprintOnDisk(t, h, ctx, sess, dir, bpPath, bpV1)
	call.Identity.ToolCallID = uuid.NewString()
	seedRaw, err := h.ToolRegistry.Run(ctx, "plan_from_blueprint", map[string]any{"blueprint_path": bpPath}, call)
	testutil.FailErr(t, "seed v1", err)
	var seeded workerplan.SeedResult
	testutil.FailErr(t, "decode seed v1", json.Unmarshal([]byte(seedRaw), &seeded))
	if len(seeded.Legs) != 2 || len(seeded.Legs[0].Criteria) != 4 || seeded.Legs[1].AfterWorkers[0] != "leg-core" {
		t.Fatalf("seeded v1 = %+v", seeded)
	}

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{"workflow_work_id": "leg-ext"}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "prerequisite_worker_not_started" {
		t.Fatalf("expected prerequisite_worker_not_started, got %v", err)
	}

	// Round 1 completes without modifying app.go or running echo wc07-ok.
	scripts.push("implementer",
		api.ToolCall{ID: "r1-read", Name: "command", Args: map[string]any{"command": "cat app.go"}},
		api.ToolCall{ID: "r1-done", Name: "complete_leg", Args: map[string]any{
			"leg_status":     "complete",
			"brief":          "Inspected app.go without editing.",
			"objectives_met": []any{"Inspected file"},
		}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{"workflow_work_id": "leg-core"}, call)
	testutil.FailErr(t, "start leg-core", err)
	testutil.FailErr(t, "drain round 1", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	verdictR1, err := h.Delegations.Plan.CheckSessionCloseout(ctx, sess.ID, dir)
	testutil.FailErr(t, "closeout after R1", err)
	if verdictR1.OK || verdictR1.Code != "COORDINATOR_CRITERIA_UNMET" {
		t.Fatalf("expected unmet criteria after R1, got %+v", verdictR1)
	}

	workers, err := h.Delegations.Records.ByBlueprint(ctx, actor, bpPath)
	testutil.FailErr(t, "by blueprint", err)
	var coreWorker workerrecords.Worker
	for _, w := range workers {
		if w.WorkflowWorkID == "leg-core" {
			coreWorker = w
		}
	}
	r1Child := coreWorker.ChildSessionID

	// Round 2 edits app.go and runs echo wc07-ok in the same child conversation.
	scripts.push("implementer",
		api.ToolCall{ID: "r2-write", Name: "command", Args: map[string]any{"command": "printf 'package app\\nvar Ready = true\\n' > app.go"}},
		api.ToolCall{ID: "r2-test", Name: "command", Args: map[string]any{"command": "echo wc07-ok", "verification": true}},
		api.ToolCall{ID: "r2-done", Name: "complete_leg", Args: map[string]any{
			"leg_status":     "complete",
			"brief":          "Updated app.go and verified.",
			"objectives_met": []any{"Modified app.go and verified"},
		}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker":       "leg-core",
		"instructions": "Update app.go and run verification.",
	}, call)
	testutil.FailErr(t, "instruct leg-core R2", err)
	testutil.FailErr(t, "drain round 2", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	reloadedCore, err := h.Delegations.Records.Worker(ctx, actor, coreWorker.ID)
	testutil.FailErr(t, "reload core worker", err)
	if reloadedCore.ChildSessionID != r1Child {
		t.Fatalf("child session changed across instruct_worker: %q != %q", reloadedCore.ChildSessionID, r1Child)
	}
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, coreWorker.ID)
	testutil.FailErr(t, "core rounds", err)
	if len(rounds) != 2 || rounds[1].ResultID == "" {
		t.Fatalf("expected 2 settled rounds, got %+v", rounds)
	}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": rounds[1].ResultID}, call)
	testutil.FailErr(t, "promote R2", err)

	// Mid-run Blueprint edit reconciles unstarted work without disturbing completed leg-core.
	bpV2 := "# Plan v2\n\n<!-- lycaon:tasks [\n" +
		`  {"id":"leg-core","title":"Core edited","files":["other.go"]},` + "\n" +
		`  {"id":"leg-ext","title":"Ext v2","files":["ext2.go"],"depends_on":["leg-core"]},` + "\n" +
		`  {"id":"leg-docs","title":"Docs","files":["README.md"]}` + "\n" +
		"] -->\n"
	approveBlueprintOnDisk(t, h, ctx, sess, dir, bpPath, bpV2)
	call.Identity.ToolCallID = uuid.NewString()
	seedV2Raw, err := h.ToolRegistry.Run(ctx, "plan_from_blueprint", map[string]any{"blueprint_path": bpPath}, call)
	testutil.FailErr(t, "seed v2", err)
	var seededV2 workerplan.SeedResult
	testutil.FailErr(t, "decode seed v2", json.Unmarshal([]byte(seedV2Raw), &seededV2))
	if len(seededV2.Legs) != 3 || seededV2.Legs[0].State != "completed" || seededV2.Legs[0].Files[0] != "app.go" || seededV2.Legs[1].Files[0] != "ext2.go" {
		t.Fatalf("reconciled v2 = %+v", seededV2)
	}

	_, err = h.Delegations.Records.CreateWorker(ctx, actor, workerrecords.Op{ID: "unrelated-op"}, workerrecords.WorkerSpec{
		Name: "side-note", AgentType: "path-explorer", ScopeMode: workerrecords.ScopeRead, Brief: "Unrelated read",
	})
	testutil.FailErr(t, "create unrelated worker", err)

	call.Identity.ToolCallID = uuid.NewString()
	boardRaw, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"plan": "wc07-flow11.md"}, call)
	testutil.FailErr(t, "pack_board plan filter", err)
	var view api.BoardView
	testutil.FailErr(t, "decode board view", json.Unmarshal([]byte(boardRaw), &view))
	if len(view.Workers) != 3 {
		t.Fatalf("filtered board workers = %d, want 3 (%+v)", len(view.Workers), view.Workers)
	}
	for _, entry := range view.Workers {
		if entry.Name == "side-note" {
			t.Fatalf("unrelated worker side-note leaked into plan-filtered board: %+v", view.Workers)
		}
	}
}

func installWC07GatedWorkflowFixture(t *testing.T, projectDir string) {
	t.Helper()
	const manifestYAML = `id: wc07-gated
version: 1.0.0
trigger: /wc07-gated
request:
  question: What change should be implemented and verified?
initial_posture: build
phases:
  - id: work
    activity_label: Implementing and reviewing
    review_loop:
      evidence_key: peer_review
      iteration_cap: 3
      assignment_binding: explicit
      required_agents: [code-reviewer]
      verdict_schema:
        verdict: APPROVED|NEEDS_REVISION
        notes: string
        coverage: coverage_review
    complete_when: gates_satisfied
    gates: [evidence_passed:peer_review, evidence_passed:verify]
    next: signoff
  - id: signoff
    activity_label: Waiting for human sign-off
    intake: [change_size]
    complete_when: gates_satisfied
    gates: [user_decision_received:change_size]
    next: done
  - id: done
    activity_label: Complete
    terminal: true
    complete_when: orchestration_complete
controls:
  phase_advance: host
`
	dstDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "wc07-gated")
	testutil.FailErr(t, "mkdir wc07-gated overlay", os.MkdirAll(dstDir, 0o755))
	testutil.FailErr(t, "write wc07-gated workflow.yaml", os.WriteFile(filepath.Join(dstDir, "workflow.yaml"), []byte(manifestYAML), 0o600))
}

func TestSameRuntimeAmbientAndGatedWorkflowReviewVerifyDecision(t *testing.T) {
	t.Run("ambient_implement", func(t *testing.T) {
		var h *Harness
		scripts := newQueuedWorkerScripts()
		h = BuildForTest(t, WithLLMClient(scripts.mock(&h)), WithoutCoordinatorLoop())
		ctx := h.OwnerCtx(t, t.Context())
		dir := h.ProjectDir(t, "wc07-ambient")
		writeTestProjectApprovalsAllowWrite(t, dir)
		testutil.FailErr(t, "seed svc.go", os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0o600))

		sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
		testutil.FailErr(t, "create ambient session", err)
		AttachDefaultAmbient(t, h, ctx, sess.ID)
		h.SeedProgress(t, ctx, sess.ID)
		testutil.FailErr(t, "orient ready", h.Workflows.Manager.Fanout.RecordBoardOrientReady(ctx, sess.ID, "orient-1"))

		call := coordToolCtx(t, h, ctx, sess, dir)
		actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}

		scripts.push("implementer",
			api.ToolCall{ID: "a-w1", Name: "command", Args: map[string]any{"command": "printf 'package svc\\nfunc V() int { return 1 }\\n' > svc.go"}},
			api.ToolCall{ID: "a-d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Draft V()", "objectives_met": []any{"Drafted V"}}},
		)
		call.Identity.ToolCallID = uuid.NewString()
		_, err = h.ToolRegistry.Run(ctx, "task", TaskToolArgs("implementer", "Write V() in svc.go.", "svc.go"), call)
		testutil.FailErr(t, "dispatch ambient implementer", err)
		testutil.FailErr(t, "drain R1", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

		ws, err := h.Delegations.Records.Workers(ctx, actor)
		testutil.FailErr(t, "workers", err)
		implW := ws[0]
		rounds, err := h.Delegations.Records.Rounds(ctx, actor, implW.ID)
		testutil.FailErr(t, "rounds R1", err)
		r1ID := rounds[0].ResultID

		scripts.push("code-reviewer",
			api.ToolCall{ID: "rev-read", Name: "command", Args: map[string]any{"command": "cat svc.go"}},
			api.ToolCall{ID: "rev-done", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Return 2 instead of 1.", "objectives_met": []any{"Reviewed R1"}}},
		)
		call.Identity.ToolCallID = uuid.NewString()
		_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{
			"agent_type": "code-reviewer",
			"review_of":  r1ID,
			"scope":      map[string]any{"mode": "read"},
			"brief":      map[string]any{"goal": "Review R1", "done_when": []any{"Review svc.go"}},
		}, call)
		testutil.FailErr(t, "dispatch reviewer", err)
		testutil.FailErr(t, "drain reviewer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

		scripts.push("implementer",
			api.ToolCall{ID: "a-w2", Name: "command", Args: map[string]any{"command": "printf 'package svc\\nfunc V() int { return 2 }\\n' > svc.go"}},
			api.ToolCall{ID: "a-d2", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Updated V() to return 2.", "objectives_met": []any{"Updated V"}}},
		)
		call.Identity.ToolCallID = uuid.NewString()
		_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{"worker": implW.Name, "instructions": "Update V() to return 2."}, call)
		testutil.FailErr(t, "instruct R2", err)
		testutil.FailErr(t, "drain R2", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

		rounds, err = h.Delegations.Records.Rounds(ctx, actor, implW.ID)
		testutil.FailErr(t, "rounds R2", err)
		call.Identity.ToolCallID = uuid.NewString()
		_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": rounds[1].ResultID}, call)
		testutil.FailErr(t, "promote ambient R2", err)
		data, err := os.ReadFile(filepath.Join(dir, "svc.go"))
		testutil.FailErr(t, "read landed svc.go", err)
		if !strings.Contains(string(data), "return 2") {
			t.Fatalf("landed svc.go = %q", string(data))
		}
	})

	t.Run("gated_workflow_obligations", func(t *testing.T) {
		runWC07GatedWorkflowSubtest(t)
	})
}

func runWC07GatedWorkflowSubtest(t *testing.T) {
	var h *Harness
	scripts := newQueuedWorkerScripts()
	h = BuildForTest(t, WithLLMClient(scripts.mock(&h)), WithoutCoordinatorLoop())
	scannersOff := false
	testutil.FailErr(t, "disable auto-scanners", h.Boards.Snapshot.SecurityScanners.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &scannersOff}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "wc07-gated")
	writeTestProjectApprovalsAllowWrite(t, dir)
	installWC07GatedWorkflowFixture(t, dir)
	testutil.FailErr(t, "seed svc.go", os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0o600))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create gated session", err)
	h.Sessions.Manager.Catalog.InvalidateEffectiveCatalog(sess.ProjectID)
	run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "wc07-gated", WorkflowVersion: "1.0.0", Request: "Implement V() with review and signoff",
	})
	testutil.FailErr(t, "start wc07-gated", err)
	h.SeedProgress(t, ctx, sess.ID)

	call := coordToolCtx(t, h, ctx, sess, dir)
	actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID}

	// Same worker/job/result exchange on the gated workflow.
	scripts.push("implementer",
		api.ToolCall{ID: "g-w1", Name: "command", Args: map[string]any{"command": "printf 'package svc\\nfunc V() int { return 2 }\\n' > svc.go"}},
		api.ToolCall{ID: "g-d1", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Wrote V().", "objectives_met": []any{"Wrote V"}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", TaskToolArgs("implementer", "Write V() in svc.go.", "svc.go"), call)
	testutil.FailErr(t, "dispatch implementer", err)
	testutil.FailErr(t, "drain implementer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	ws, err := h.Delegations.Records.Workers(ctx, actor)
	testutil.FailErr(t, "workers", err)
	rounds, err := h.Delegations.Records.Rounds(ctx, actor, ws[0].ID)
	testutil.FailErr(t, "rounds", err)
	r1ID := rounds[0].ResultID

	// Unbound required reviewer is refused; undeclared reviews cannot satisfy workflow gates.
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{
		"agent_type": "code-reviewer",
		"review_of":  r1ID,
		"scope":      map[string]any{"mode": "read"},
		"brief":      map[string]any{"goal": "Unbound required reviewer", "done_when": []any{"Check svc.go"}},
	}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "review_work_id_required" {
		t.Fatalf("expected review_work_id_required on unbound code-reviewer, got %v", err)
	}

	scripts.push("skeptic",
		api.ToolCall{ID: "u-read", Name: "command", Args: map[string]any{"command": "cat svc.go"}},
		api.ToolCall{ID: "u-done", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Unbound persona review.", "objectives_met": []any{"Checked"}, "findings": []any{map[string]any{"path": "svc.go", "line": 1, "excerpt": "package svc"}}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{
		"agent_type": "skeptic",
		"review_of":  r1ID,
		"scope":      map[string]any{"mode": "read"},
		"brief":      map[string]any{"goal": "Unbound persona review", "done_when": []any{"Check svc.go"}},
	}, call)
	testutil.FailErr(t, "dispatch unbound reviewer", err)
	testutil.FailErr(t, "drain unbound reviewer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "promote_overlay", map[string]any{"overlay_id": r1ID}, call)
	testutil.FailErr(t, "promote R1", err)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, "## Progress\n- [x] implemented\n"), call)
	testutil.FailErr(t, "update_progress", err)

	coverageArg := func(cite api.CitationGroundingCitedEvidence) map[string]any {
		cur, gErr := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
		testutil.FailErr(t, "read coverage run", gErr)
		manifest, mErr := h.Workflows.Manager.Resolver.ForRun(ctx, cur)
		testutil.FailErr(t, "read coverage manifest", mErr)
		facts, fErr := h.Workflows.Manager.Coverage.CoverageFacts(ctx, cur, manifest)
		testutil.FailErr(t, "read coverage facts", fErr)
		rev := completeCoverageReview(facts, cite)
		if rev.Assessments == nil {
			rev.Assessments = []api.CoverageAssessment{}
		}
		raw, jErr := json.Marshal(rev)
		testutil.FailErr(t, "encode coverage review", jErr)
		var out map[string]any
		testutil.FailErr(t, "decode coverage review", json.Unmarshal(raw, &out))
		return out
	}

	call.Identity.ToolCallID = uuid.NewString()
	summaryRaw, err := h.ToolRegistry.Run(ctx, "read_review", map[string]any{"view": "summary"}, call)
	testutil.FailErr(t, "read_review summary", err)
	if !strings.Contains(summaryRaw, "review/code-reviewer") {
		t.Fatalf("read_review summary missing review/code-reviewer: %s", summaryRaw)
	}
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{
			"verdict":  "APPROVED",
			"notes":    "Attempt before assigned review",
			"coverage": coverageArg(api.CitationGroundingCitedEvidence{Path: "svc.go", Line: 1, Excerpt: "package svc"}),
		},
		"cited_evidence": []any{map[string]any{"path": "svc.go", "line": float64(1), "excerpt": "package svc"}},
	}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "SUBMIT_VERDICT_REVIEWER_MISSING" {
		t.Fatalf("expected SUBMIT_VERDICT_REVIEWER_MISSING after unbound persona, got %v", err)
	}

	blockedResp, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Close out now.")
	testutil.FailErr(t, "prompt blocked closeout", err)
	if blockedResp == nil || blockedResp.Outcome != promptresult.OutcomeCloseoutBlocked {
		t.Fatalf("expected closeout_blocked while gates remain open, got %+v", blockedResp)
	}

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "read_review", map[string]any{"view": "subject", "assignment_id": "any"}, call)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "READ_REVIEW_INVALID" {
		t.Fatalf("expected READ_REVIEW_INVALID on coordinator subject read, got %v", err)
	}

	// Dispatch the workflow-assigned reviewer worker and satisfy review + verify + human decision.
	scripts.push("code-reviewer",
		api.ToolCall{ID: "asgn-read", Name: "command", Args: map[string]any{"command": "cat svc.go"}},
		api.ToolCall{ID: "asgn-done", Name: "complete_leg", Args: map[string]any{"leg_status": "complete", "brief": "Assigned review passed.", "objectives_met": []any{"Verified"}, "findings": []any{map[string]any{"path": "svc.go", "line": 1, "excerpt": "package svc"}}}},
	)
	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "task", map[string]any{
		"workflow_work_id": "review/code-reviewer",
		"agent_type":       "code-reviewer",
		"scope":            map[string]any{"mode": "read"},
		"brief":            map[string]any{"goal": "Assigned workflow review", "done_when": []any{"Review svc.go"}},
	}, call)
	testutil.FailErr(t, "dispatch assigned reviewer", err)
	testutil.FailErr(t, "drain assigned reviewer", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))

	call.Identity.ToolCallID = uuid.NewString()
	assignRaw, err := h.ToolRegistry.Run(ctx, "read_review", map[string]any{"view": "assignments"}, call)
	testutil.FailErr(t, "read_review assignments", err)
	if !strings.Contains(assignRaw, "code-reviewer") {
		t.Fatalf("read_review assignments = %s", assignRaw)
	}

	runTasks, err := h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "list workflow tasks", err)
	var revChildID string
	for _, rt := range runTasks {
		if rt.WorkflowWorkID == "review/code-reviewer" {
			revChildID = rt.ChildSessionID
		}
	}
	if revChildID == "" {
		t.Fatalf("missing assigned reviewer child session in %+v", runTasks)
	}
	revCite := api.CitationGroundingCitedEvidence{Handle: revChildID + ":read#1", Path: "svc.go", Excerpt: "package svc"}

	vars, err := h.Workflows.Manager.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetGateSatisfied(vars, "evidence_passed:verify", true)
	curRun, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get current run", err)
	testutil.FailErr(t, "update verify gate", h.Workflows.Manager.Store.State.UpdateVars(ctx, curRun, dir, vars))

	call.Identity.ToolCallID = uuid.NewString()
	_, err = h.ToolRegistry.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{
			"verdict":  "APPROVED",
			"notes":    "Independent review and verification complete.",
			"coverage": coverageArg(revCite),
		},
		"cited_evidence": []any{map[string]any{"handle": revCite.Handle, "path": "svc.go", "excerpt": "package svc"}},
	}, call)
	if rej := toolrejection.AsToolReject(err); rej != nil {
		t.Fatalf("submit_verdict after assigned review rejected: %s data=%+v tasks=%+v", rej.Code, rej.Data, runTasks)
	}
	testutil.FailErr(t, "submit_verdict after assigned review", err)
	_, _ = h.Workflows.Manager.Phases.TryAutoAdvance(ctx, run.ID)

	waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "signoff")

	// Stale-phase dispatch is refused while prior results remain intact.
	stale := api.WorkerTask{
		ID: uuid.NewString(), ProjectID: sess.ProjectID, ParentSessionID: sess.ID, WorkspacePath: dir,
		WorkflowRunID: run.ID, WorkflowPhase: "work", WorkflowWorkID: "review/code-reviewer",
		AgentType: "code-reviewer", Prompt: "stale", Brief: "stale", SourceToolCallID: uuid.NewString(),
	}
	prepErr := h.Delegations.Queue.PrepareEnqueue(ctx, sess.ProjectID, &stale)
	if rej := toolrejection.AsToolReject(prepErr); rej == nil || rej.Data["reason"] != "workflow_phase_changed" {
		t.Fatalf("expected workflow_phase_changed on stale phase, got %v (rej=%v)", prepErr, rej)
	}
	priorTasks, err := h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "list prior tasks after stale attempt", err)
	if len(priorTasks) != 3 {
		t.Fatalf("prior tasks count = %d, want 3 intact", len(priorTasks))
	}

	_, err = h.Workflows.Manager.Feedback.ResolveUserFeedback(ctx, sess.ID, run.ID, "signoff", "small")
	testutil.FailErr(t, "ResolveUserFeedback signoff", err)
	finalRun, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "load final run", err)
	if finalRun.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("final workflow status = %q, want complete", finalRun.Status)
	}
}
