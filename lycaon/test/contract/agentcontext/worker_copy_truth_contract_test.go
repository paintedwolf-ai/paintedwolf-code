package contract

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkerCopyStateMatrixAndReachability(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	catalog, profiles := promptNameCatalog(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	cells := derivedWorkerStateMatrix()
	if len(cells) == 0 {
		t.Fatal("empty derived worker state matrix")
	}

	for id, plan := range plans {
		reachable := plan.AddressableNames()
		toolVars := prompts.CoordinatorPolicyTemplateVars(reachable, nil)
		for _, cell := range cells {
			for _, ws := range []string{"retained", "refreshed", "reset", "recovering"} {
				row := rosterMapForCell(cell, "alpha-worker", "res-alpha-1", ws)
				block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-matrix", inject.TurnStateInjectInput{
					Workers:  []map[string]any{row},
					ToolVars: toolVars,
				})
				contractcheck.FailErr(t, "render turn state", err)
				if names := unreachableNames(t, catalog, block, reachable); len(names) > 0 {
					t.Errorf("%s/%s names unreachable tools %v:\n%s", id, cell.name, names, linesNaming(block, names))
				}
				checkBannedMention(t, retiredToolMention, id+"/"+cell.name, block)
			}
		}
		for _, noticeKind := range []string{"consumption", "quiet"} {
			data := map[string]any{
				"worker": "alpha-worker", "agent_type": "implementer", "round_ordinal": 2,
				"notice_kind": noticeKind, "model_calls": 60, "tokens": 24000, "estimated_spend_usd": "0.18",
				"quiet_minutes": 5, "in_flight": "tools", "tools_done": 1, "tool_calls": 3,
				"latest_message_seq": 4,
			}
			contractcheck.FailErr(t, "merge kick vars", prompts.MergeCoordinatorKickVarsForTools(data, reachable))
			out, err := engine.RenderKick(context.Background(), "coordinator-worker-round-notice", data)
			contractcheck.FailErr(t, "render round notice", err)
			if names := unreachableNames(t, catalog, out, reachable); len(names) > 0 {
				t.Errorf("round notice on %s names unreachable %v", id, names)
			}
			checkBannedMention(t, retiredToolMention, "round-notice/"+id, out)
		}
	}

	for _, profile := range profiles {
		if !profile.Tools["complete_leg"] {
			continue
		}
		var reachable []string
		for name, enabled := range profile.Tools {
			if enabled {
				reachable = append(reachable, name)
			}
		}
		for _, viewKind := range []string{"materialized", "mounted"} {
			for _, hasGit := range []bool{false, true} {
				out, err := inject.RenderWorkerTaskAssignment(context.Background(), renderer, inject.WorkerTaskAssignmentInput{
					SessionID: "child-matrix", ProjectDir: root, WorkerName: "alpha-worker",
					RoundInstructions: "Check edge cases.", ViewKind: viewKind, HasGit: hasGit,
					AgentType: "implementer", WorkerJobID: "job-matrix",
					Scope:   api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"lycaon/internal/workerstate"}},
					Charter: api.WorkerTaskCharter{Goal: "Validate matrix", DoneWhen: []string{"Tests pass"}},
				})
				contractcheck.FailErr(t, "render worker assignment", err)
				if names := unreachableNames(t, catalog, out, reachable, string(api.TaskScopeModeWrite)); len(names) > 0 {
					if !(slices.Equal(names, []string{"scan_query"}) && !profile.Tools["scan_query"]) &&
						!(slices.Contains(names, "read_findings") && !profile.Tools["read_findings"]) {
						t.Errorf("assignment on %s names unreachable %v", profile.ID, names)
					}
				}
				checkBannedMention(t, retiredToolMention, "assignment/"+profile.ID, out)
			}
		}
	}

	policyDirs, err := filepath.Glob(filepath.Join(root, "lycaon/config/packs/painted-wolf/*/policy"))
	contractcheck.FailErr(t, "glob policy dirs", err)
	results, err := oar.CheckRuleFiles(context.Background(), policyDirs, oar.RuleCheckOptions{
		SchemaDir: filepath.Join(root, "schemas"),
	})
	contractcheck.FailErr(t, "check OAR rules", err)
	for _, res := range results {
		if !res.OK() {
			t.Errorf("OAR rule %s (%s) failed: %s (%s)", res.RuleID, res.Path, res.Code, res.Detail)
		}
	}
}

func TestWorkerCopyAdmissionAgreement(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	toolVars := prompts.CoordinatorPolicyTemplateVars(plans[surface.SurfaceImplementOverlayPromote].AddressableNames(), nil)

	for _, cell := range derivedWorkerStateMatrix() {
		if cell.view.State == workerstate.Delivered && !cell.facts.OpenChanges {
			continue
		}
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-admit", inject.TurnStateInjectInput{
			Workers:  []map[string]any{rosterMapForCell(cell, "w-admit", "res-admit", "retained")},
			ToolVars: toolVars,
		})
		contractcheck.FailErr(t, "render turn state", err)
		for tool, op := range workerToolOps {
			if strings.Contains(block, "`"+tool+"`") && !cell.view.Admits(op) {
				t.Errorf("cell %s (%s/%s) offered %s (%s) which state does not admit (ops=%v)",
					cell.name, cell.view.State, cell.view.Reason, tool, op, cell.view.Ops)
			}
		}
	}

	nextStepsDir := filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/next-steps")
	refusalStates := map[string]workerstate.Facts{
		"INSTRUCT_WORKER_ASKING":  {Latest: &workerstate.Round{DecisionPending: true}},
		"INSTRUCT_WORKER_WAITING": {Latest: &workerstate.Round{Held: true}},
		"INSTRUCT_WORKER_PLANNED": {Planned: true, Role: workerstate.RolePlanned},
		"INSTRUCT_WORKER_AGENT_CHANGES_OPEN": {
			Latest: &workerstate.Round{Settled: true, Reported: true}, OpenChanges: true,
		},
		"INSTRUCT_WORKER_AGENT_SEALED": {
			Role: workerstate.RoleReviewer, Latest: &workerstate.Round{Settled: true, Reported: true},
		},
	}
	stepOp := map[string]workerstate.Operation{
		"answer_decision": workerstate.OpAnswer,
		"worker_cancel":   workerstate.OpWithdraw,
		"task":            workerstate.OpStart,
		"promote_overlay": workerstate.OpPromote,
		"reject_overlay":  workerstate.OpReject,
		"instruct_worker": workerstate.OpInstruct,
	}
	for code, facts := range refusalStates {
		raw := contractcheck.ReadRepoFile(t, root, filepath.ToSlash(filepath.Join("lycaon/config/packs/painted-wolf/platform/host/next-steps", code+".yaml")))
		ns, err := oar.DecodeNextSteps([]byte(raw))
		contractcheck.FailErr(t, "decode next steps "+code, err)
		view := workerstate.Derive(facts)
		for _, step := range ns.Steps {
			if code == "INSTRUCT_WORKER_AGENT_SEALED" && step.Tool == "task" {
				continue
			}
			if op, ok := stepOp[step.Tool]; ok && !view.Admits(op) {
				t.Errorf("%s recovery offers %s (%s), not admitted in %v", code, step.Tool, op, view.Ops)
			}
		}
	}
	_ = nextStepsDir

	sqlDB, records, svc, jobs, decs, actor := newAdmissionService(t)
	ts := db.FormatTime(time.Now().UTC())
	w, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "mk"}, workerrecords.WorkerSpec{
		Name: "w-live", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child",
	})
	contractcheck.FailErr(t, "create worker", err)
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('j1', ?, 'coord', 'child', 'implementer', 'running', 'p', 'b', ?)`, testdbseed.DefaultProjectID, ts)
	contractcheck.FailErr(t, "insert j1", err)
	jobs["j1"] = &api.WorkerTask{ID: "j1", Status: api.WorkerStatusRunning}
	_, err = records.AdmitRound(t.Context(), actor, workerrecords.Op{ID: "r1"}, w.WorkerID, workerrecords.RoundSpec{JobID: "j1", ExecutionTarget: "local"})
	contractcheck.FailErr(t, "admit r1", err)

	decs["child"] = api.WorkerDecisionRequest{Question: "port?"}
	_, err = svc.Instruct(t.Context(), actor, workerrecords.Op{ID: "i-ask"}, workerops.Instruction{Worker: "w-live", Instructions: "go"}, nil)
	var ref *workerops.Refusal
	if !errors.As(err, &ref) || ref.View.State != workerstate.Asking || !ref.View.Admits(workerstate.OpAnswer) {
		t.Fatalf("expected asking refusal admitting answer, got %v", err)
	}
	delete(decs, "child")
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-j1', 'j1', 'complete', ?)`, ts)
	contractcheck.FailErr(t, "insert res-j1", err)
	_, err = db.New(sqlDB).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(ts), CheckpointID: "cp-1", JobID: "j1",
	})
	contractcheck.FailErr(t, "settle r1", err)
	jobs["j1"] = &api.WorkerTask{ID: "j1", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}

	disp, err := svc.Shelve(t.Context(), actor, workerrecords.Op{ID: "sh-1"}, "res-j1", "defer cleanup")
	contractcheck.FailErr(t, "shelve admitted on open changes", err)
	if disp.ChangeState != "shelved" {
		t.Fatalf("disposition = %q, want shelved", disp.ChangeState)
	}
	enqueue := func(ctx context.Context, rj workerops.RoundJob) (string, string, error) {
		_, e := sqlDB.ExecContext(ctx, `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
			VALUES ('j2', ?, 'coord', 'child', ?, 'pending', 'p', 'b', ?)`, testdbseed.DefaultProjectID, rj.Worker.AgentType, ts)
		jobs["j2"] = &api.WorkerTask{ID: "j2", Status: api.WorkerStatusPending}
		return "j2", "local", e
	}
	rec, err := svc.Instruct(t.Context(), actor, workerrecords.Op{ID: "i-idle"}, workerops.Instruction{Worker: "w-live", Instructions: "next round"}, enqueue)
	contractcheck.FailErr(t, "instruct admitted on idle worker", err)
	if rec.Outcome != workerops.OutcomeStarted {
		t.Fatalf("outcome = %q, want started", rec.Outcome)
	}
	_ = sandbox.ToolProfile{}
}

func TestWorkerCopyInterpolationSensitivity(t *testing.T) {
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)

	b1, err := inject.RenderTurnStateBlock(context.Background(), renderer, "s1", inject.TurnStateInjectInput{
		Workers: []map[string]any{{"name": "worker-one", "agent": "implementer", "state": "delivered", "result_handle": "res-one", "result_disposition": "open"}},
	})
	contractcheck.FailErr(t, "turn state 1", err)
	b2, err := inject.RenderTurnStateBlock(context.Background(), renderer, "s2", inject.TurnStateInjectInput{
		Workers: []map[string]any{{"name": "worker-two", "agent": "implementer", "state": "delivered", "result_handle": "res-two", "result_disposition": "open"}},
	})
	contractcheck.FailErr(t, "turn state 2", err)
	if b1 == b2 || !strings.Contains(b1, "worker-one") || !strings.Contains(b1, "res-one") || strings.Contains(b1, "worker-two") || strings.Contains(b1, "res-two") {
		t.Fatalf("turn-state interpolation failed:\nb1=%s\nb2=%s", b1, b2)
	}

	m1 := inject.RenderWorkerMessagesBlock(context.Background(), renderer, "s1", "coordinator", []inject.WorkerMessageLine{
		{Worker: "scout-alpha", Seq: 11, NeedsReply: true, Body: "payload-alpha"},
	}, false)
	m2 := inject.RenderWorkerMessagesBlock(context.Background(), renderer, "s2", "coordinator", []inject.WorkerMessageLine{
		{Worker: "scout-beta", Seq: 22, NeedsReply: true, Body: "payload-beta"},
		{Worker: "scout-beta", Seq: 23, Body: "second-beta"},
	}, true)
	if m1.Header == m2.Header || m1.Messages == m2.Messages || !strings.Contains(m1.Messages, "scout-alpha") || strings.Contains(m1.Messages, "scout-beta") {
		t.Fatalf("worker-messages interpolation failed:\nm1=%+v\nm2=%+v", m1, m2)
	}

	r1 := inject.RenderWorkerRoundBlock(context.Background(), renderer, "c1", inject.WorkerRoundFrame{
		Ordinal: 2, Brief: "brief-one", Workspace: "refreshed", EarlierResults: []string{"res-old-1"},
	})
	r2 := inject.RenderWorkerRoundBlock(context.Background(), renderer, "c2", inject.WorkerRoundFrame{
		Ordinal: 5, Brief: "brief-two", Workspace: "reset", EarlierResults: []string{"res-old-9"},
	})
	if r1.Header == r2.Header || !strings.Contains(r1.Header, "res-old-1") || strings.Contains(r1.Header, "res-old-9") {
		t.Fatalf("worker-round interpolation failed:\nr1=%+v\nr2=%+v", r1, r2)
	}
}
