package promptrender

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/internal/worker"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func renderActiveWorkflowInjectVariants(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	cReps []coordRep,
	resolver *namedWorkerResolver,
	sum *CartesianSummary,
) error {
	gateCfg, err := feedback.LoadGateFeedbackCatalogWithCatalog(s.Catalog)
	if err != nil {
		return err
	}
	hintCfg := &guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
		"WORKFLOW_GATE_UNMET": {Message: "Gate pending.", Fix: "Complete open obligations."},
		"INFO_ONLY":           {Message: "Note only.", Fix: ""},
	}}
	frames := activeWorkflowFrames()
	for _, i := range distinctReaderIndexes(len(cReps), func(i int) prompts.Reader { return cReps[i].auth.Reader }) {
		cr := cReps[i]
		if !cr.state.TurnProfile.WorkflowActive {
			continue
		}
		resolveNamed := resolver.forCoordinatorState(cr.state)
		for fIdx, fr := range frames {
			var codes []string
			if fIdx == 0 {
				codes = []string{"WORKFLOW_GATE_UNMET", "INFO_ONLY"}
			}
			if !workflowFrameCompatibleWithAuthority(fr, cr.auth) {
				continue
			}
			block, rErr := inject.RenderActiveWorkflowInject(ctx, injRenderer, cr.auth.Reader, "sess-wf", fr, hintCfg, codes, gateCfg)
			if rErr != nil {
				return rErr
			}
			sum.InjectAndGuidanceCount++
			if block != "" {
				viols := checkClaimsOnce(s, fmt.Sprintf("inject/active-workflow[%d] (%s)", fIdx, cr.state.ID), block, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}
	}
	wfReg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return err
	}
	pairs, err := cachedCoordinatorPairs()
	if err != nil {
		return err
	}
	byBinding := boundRecipients(pairs)
	for _, key := range sortedKeys(wfReg.All()) {
		m := wfReg.All()[key]
		var rows []inject.WorkflowPhaseRow
		for _, p := range m.PhaseDefs {
			rows = append(rows, inject.WorkflowPhaseRow{ID: p.ID, CompleteWhen: p.CompleteWhen, Next: p.Next, Terminal: p.Terminal})
		}
		for _, p := range m.PhaseDefs {
			b, bErr := hoststate.BindPhase(m, p)
			if bErr != nil {
				return bErr
			}
			exit := workflowpresentation.ProjectPhaseExit(m, p, nil, nil).InjectView()
			for _, cr := range byBinding[b.Key()] {
				runCtx := cr.state.RunContext
				runCtx.WorkflowID, runCtx.WorkflowVersion, runCtx.CurrentPhase = m.ID, m.Version, p.ID
				runCtx.RunStatus = string(api.WorkflowRunStatusRunning)
				fr := inject.CoordinatorTurnFrame{
					RunContext: runCtx,
					Runtime:    inject.WorkflowRuntimeSnapshot{Phases: rows, PhaseExit: exit},
				}
				block, rErr := inject.RenderActiveWorkflowInject(ctx, injRenderer, cr.auth.Reader, "sess-wf-cat", fr, nil, nil, gateCfg)
				if rErr != nil {
					return rErr
				}
				sum.InjectAndGuidanceCount++
				resolveNamed := resolver.forCoordinatorState(cr.state)
				viols := checkClaimsOnce(s, fmt.Sprintf("inject/active-workflow/%s/%s (%s)", m.ID, p.ID, cr.state.ID), block, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}
	}
	return renderWorkflowFramesOnBoundSurfaces(ctx, s, injRenderer, wfReg, cReps, gateCfg, hintCfg, resolver, sum)
}

func workflowFrameCompatibleWithAuthority(fr inject.CoordinatorTurnFrame, auth hoststate.TurnAuthority) bool {
	pe := fr.Runtime.PhaseExit
	if pe == nil {
		return true
	}
	if pe.Kind == "review_loop" && !auth.Plan.Addressable("submit_verdict") {
		return false
	}
	if pe.CoordinatorAdvances && !auth.Plan.Addressable("workflow_advance") {
		return false
	}
	for _, arm := range pe.ChoiceTransitions {
		if arm.CoordinatorMayFire && !auth.Plan.Addressable("workflow_transition") {
			return false
		}
	}
	return true
}

// renderWorkflowFramesOnBoundSurfaces renders each runtime frame on every
// coordinator recipient of the surface its shipped workflow binds to the
// frame's phase.
func renderWorkflowFramesOnBoundSurfaces(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	wfReg *workflowdef.Registry,
	cReps []coordRep,
	gateCfg *feedback.GateFeedbackCatalog,
	hintCfg *guidance.HintConfig,
	resolver *namedWorkerResolver,
	sum *CartesianSummary,
) error {
	boundSurface := func(workflowID, phaseID string) string {
		for _, m := range wfReg.All() {
			if m.ID != workflowID {
				continue
			}
			for _, p := range m.PhaseDefs {
				if p.ID == phaseID {
					return p.CoordinatorSurface
				}
			}
		}
		return ""
	}
	for i, fr := range activeWorkflowFrames() {
		surfID := boundSurface(fr.RunContext.WorkflowID, fr.RunContext.CurrentPhase)
		if surfID == "" {
			continue
		}
		var codes []string
		if i == 0 {
			codes = []string{"WORKFLOW_GATE_UNMET", "INFO_ONLY"}
		}
		for _, cr := range cReps {
			if cr.state.TurnProfile.SurfaceID != surfID || !workflowFrameCompatibleWithAuthority(fr, cr.auth) {
				continue
			}
			block, err := inject.RenderActiveWorkflowInject(ctx, injRenderer, cr.auth.Reader, "sess-wf-bound", fr, hintCfg, codes, gateCfg)
			if err != nil {
				return err
			}
			sum.InjectAndGuidanceCount++
			resolveNamed := resolver.forCoordinatorState(cr.state)
			viols := checkClaimsOnce(s, fmt.Sprintf("inject/active-workflow-bound[%d] (%s)", i, cr.state.ID), block, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

func activeWorkflowFrames() []inject.CoordinatorTurnFrame {
	return []inject.CoordinatorTurnFrame{
		{
			Roster: &inject.AgentRoster{
				Effective: []string{"skeptic", "code-reviewer"},
				Excluded:  []inject.ExcludedAgent{{Name: "implementer", Code: "SURFACE_READ_ONLY"}},
			},
			RunContext: api.CoordinatorRunContext{
				WorkflowID: "security-survey", WorkflowVersion: "3.0.0", RunID: "run-1", RunStatus: "running",
				CurrentPhase: "challenge", RequiresIsolation: true, CoordinatorBrief: "Verify claims.",
				FailedLeaves:    []string{"evidence_passed:survey_challenged", "obligation_settled:scan"},
				PendingFeedback: &api.PendingFeedback{PhaseID: "challenge", Prompt: "Focus on auth?"},
				Request:         &api.WorkflowRequestState{Status: "active", Text: "Audit auth", Source: "user", Sequence: 1},
				FeedbackPhases:  []api.ComposeFeedbackPhase{{ID: "intake"}},
				DecisionPhases:  []api.ComposeDecisionPhase{{ID: "challenge"}},
			},
			Runtime: inject.WorkflowRuntimeSnapshot{
				ReportDocumentEnabled: true,
				CoverageReview:        "1 gap assessed",
				ReportRating:          &inject.ReportRatingView{Question: "Overall risk?", Levels: "high, low", Dimensions: []string{"auth", "crypto"}, Questions: "auth: high|low"},
				Topology:              "fanout",
				FanoutPlan:            "2 legs active",
				BlueprintApproval:     &inject.BlueprintApprovalView{Status: "approved", Origin: "human", ParentRunID: "parent-1"},
				Phases: []inject.WorkflowPhaseRow{
					{ID: "challenge", BindTopologyStage: "verify", Next: "synthesis", Gates: []inject.WorkflowGateState{
						{ID: "evidence_passed:survey_challenged", Satisfied: false},
						{ID: "obligation_settled:scan", Satisfied: true},
					}},
					{ID: "synthesis", Terminal: true},
				},
				PhaseExit: &inject.PhaseExitView{
					Kind: "review_loop", DepthParam: "thorough", CoordinatorAdvances: false,
					ReviewWorkIDs: map[string]string{"skeptic": "work-1"}, ReviewAgents: []string{"skeptic", "code-reviewer"},
					VerdictOutline: "{status: pass|fail}", ReviewLoopKey: "survey_challenged", ReviewLoopCap: 3, FollowupAttempts: 1,
				},
			},
		},
		{
			RunContext: api.CoordinatorRunContext{
				WorkflowID: "plan", RunID: "run-2", RunStatus: "running", CurrentPhase: "draft",
				Request:         &api.WorkflowRequestState{Status: "active"},
				PendingFeedback: &api.PendingFeedback{PhaseID: "draft"},
			},
			Runtime: inject.WorkflowRuntimeSnapshot{
				ReportDocumentEnabled: true,
				BlueprintApproval:     &inject.BlueprintApprovalView{Status: "invalid"},
				Phases: []inject.WorkflowPhaseRow{
					{ID: "draft", Next: "approve", Gates: []inject.WorkflowGateState{
						{ID: "plan_stub_valid", Satisfied: false},
						{ID: "hitl_consulted:intake", Satisfied: false},
					}},
					{ID: "approve", Terminal: true},
				},
				PhaseExit: &inject.PhaseExitView{
					Kind: "gates", CoordinatorAdvances: true,
					OpenGates:    []string{"plan_stub_valid", "hitl_consulted:intake"},
					DormantGates: []string{"dormant_a", "dormant_b"},
					ChoiceTransitions: []inject.PhaseExitChoiceArm{
						{ID: "revise", Label: "Revise", Actors: []string{"coordinator", "human"}, CoordinatorMayFire: true},
						{ID: "park", Label: "Park", Actors: nil, CoordinatorMayFire: false},
					},
				},
			},
		},
		{
			RunContext: api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "approve"},
			Runtime: inject.WorkflowRuntimeSnapshot{
				BlueprintApproval: &inject.BlueprintApprovalView{Status: "pending"},
				Phases:            []inject.WorkflowPhaseRow{{ID: "approve", CompleteWhen: "human approves"}},
				PhaseExit:         &inject.PhaseExitView{Kind: "human_approval", HumanApproval: true},
			},
		},
		{
			RunContext: api.CoordinatorRunContext{WorkflowID: "compose", CurrentPhase: "invoke"},
			Runtime: inject.WorkflowRuntimeSnapshot{
				Phases:    []inject.WorkflowPhaseRow{{ID: "invoke", CompleteWhen: "child completes"}},
				PhaseExit: &inject.PhaseExitView{Kind: "invoke", InvokeWorkflowID: "implement", CoordinatorAdvances: false, OpenGates: []string{"child_run_complete"}, DormantGates: []string{"only_one"}},
			},
		},
		{
			RunContext: api.CoordinatorRunContext{WorkflowID: "simple", CurrentPhase: "work"},
			Runtime: inject.WorkflowRuntimeSnapshot{
				Phases:    []inject.WorkflowPhaseRow{{ID: "work", CompleteWhen: "draft ready"}},
				PhaseExit: &inject.PhaseExitView{Kind: "open", CompleteWhen: "draft ready", CoordinatorAdvances: false},
			},
		},
		{
			RunContext: api.CoordinatorRunContext{},
			Runtime: inject.WorkflowRuntimeSnapshot{
				Phases:    []inject.WorkflowPhaseRow{{ID: "done", Terminal: true}},
				PhaseExit: &inject.PhaseExitView{Kind: "terminal"},
			},
		},
	}
}

func renderStructuralInjectVariants(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	cr coordRep,
	sum *CartesianSummary,
) error {
	zeroAuth := zeroToolAuthority(cr.auth)
	cases := []struct {
		ref  string
		data map[string]any
	}{
		{"agents-md", map[string]any{"mode": "index", "index": []map[string]any{{"path": "AGENTS.md", "bytes": 120}}}},
		{"agents-md", map[string]any{"mode": "scope_limit", "limit": 4}},
		{"agents-md", map[string]any{"mode": "context_limit", "limit": 8192}},
		{"agents-md", map[string]any{"mode": "unavailable"}},
		{"agents-md", map[string]any{"mode": "chain", "chain": []map[string]any{{"path": "AGENTS.md", "content": "Follow rules."}, {"path": "sub/AGENTS.md", "content": ""}}}},
		{"agents-md", map[string]any{"mode": "none"}},
		{"blueprint", map[string]any{"blueprint_path": "plan.md", "frontmatter": []map[string]any{{"key": "goal", "value": "ship"}}, "body": "## Goal\nShip."}},
		{"blueprint", map[string]any{"body": "Plain blueprint."}},
		{"execution-state", map[string]any{
			"view": "instructions", "declared_command": "./task test:digest",
			"operations":         []map[string]any{{"tool": "edit", "path": "a.go", "command": "edit", "invoked": true, "checklist_after": true, "tool_request_after": true}, {"tool": "write"}},
			"operations_omitted": 1,
			"executions":         []map[string]any{{"code": "PASS", "command": "./task test:short", "cwd": "@primary", "resolvable": true, "source_verdict": "clean", "source_revision": "abc", "source_root_digest": "def"}, {"summary": "min"}},
			"executions_omitted": 1,
		}},
		{"execution-state", map[string]any{"view": "summary"}},
		{"scan-guidance-ephemeral", map[string]any{"summaries": []map[string]any{{"rule": "R1", "fix": "Validate input.", "count": 2}, {"rule": "R2", "count": 1}}}},
		{"synthesis-evidence", map[string]any{"execution_observations": "1 test run passed."}},
		{"synthesis-evidence", map[string]any{}},
		{"view-context", map[string]any{
			"folder": "src/", "search_query": "main", "search_hit": "src/main.go:10",
			"editors": []map[string]any{
				{"path": "a.go", "unsaved": true, "selected": true, "start_line": 1, "end_line": 5},
				{"path": "b.go", "unsaved": false, "selected": false, "cursor": true, "line": 3},
				{"path": "c.go", "selected": true, "start_line": 8, "end_line": 8},
			},
			"diffs":    []map[string]any{{"path": "a.go"}},
			"findings": []map[string]any{{"rule_id": "R1", "level": "high", "location": "a.go:4"}, {"rule_id": "R2", "level": "low"}},
		}},
		{"view-context", map[string]any{}},
		{"worker-messages", map[string]any{"recipient": "coordinator", "message_count": 2, "has_needs_reply": true, "more": 1, "messages": []map[string]any{{"from": "w1", "body": "Ready."}}}},
		{"worker-messages", map[string]any{"recipient": "worker", "message_count": 1, "has_needs_reply": false, "more": 0}},
		{"worker-round", map[string]any{"has_brief": true, "instruction_count": 2, "workspace": "refreshed", "prior_change": "shelved", "earlier_results": []string{"r1", "r2"}}},
		{"worker-round", map[string]any{"has_brief": true, "instruction_count": 0, "workspace": "reset", "prior_change": "open"}},
		{"worker-round", map[string]any{"has_brief": false, "workspace": "retained", "prior_change": "none"}},
		{"worker-round", map[string]any{"has_brief": false, "workspace": "fresh", "prior_change": "none"}},
	}
	for i, tc := range cases {
		out, err := injRenderer.Render(ctx, tc.ref, tc.data)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if strings.TrimSpace(out) != "" {
			viols := checkClaimsOnce(s, fmt.Sprintf("structural-inject/%s[%d]", tc.ref, i), out, zeroAuth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

func runWorkerInjectRenders(
	ctx context.Context,
	s *Stock,
	wReps []workerRep,
	sum *CartesianSummary,
) error {
	injRenderer := prompts.NewInjectRenderer(s.Engine())
	now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	jobs := []bgprocess.JobSnapshot{
		{Handle: "wcmd-1", Mode: bgprocess.JobModeAwaited, OriginTool: "command", StartedAt: now.Add(-10 * time.Second), Timeout: time.Minute, Stages: []hostcmd.StageResult{{Command: "ls"}}},
		{Handle: "wcmd-2", Mode: bgprocess.JobModeBackground, OriginTool: "command", StartedAt: now.Add(-time.Minute), Exited: true, ExitCode: 0},
	}

	firstOfReader := map[int]bool{}
	for _, i := range distinctReaderIndexes(len(wReps), func(i int) prompts.Reader { return wReps[i].auth.Reader }) {
		firstOfReader[i] = true
	}
	for idx, wr := range wReps {
		waitSchema := wr.auth.Schemas["wait"]
		wCmd := inject.RenderCommandJobsBlock(ctx, injRenderer, wr.auth.Reader, waitSchema, "wsess", jobs, nil, now, "worker")
		if wCmd != "" {
			sum.WorkerArtifactCount++
			viols := checkClaimsOnce(s, "worker/command-jobs ("+wr.ws.ID+")", wCmd, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}

		// RenderWorkerBoardInject
		bText, err := inject.RenderWorkerBoardInject(ctx, injRenderer, wr.auth.Reader, "wsess", api.BoardSnapshot{
			Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 12, Layout: api.RepoLayout{Files: []string{"main.go"}}},
		}, packboard.InjectScopeFull, true, false, now)
		if err != nil {
			return err
		}
		if bText != "" {
			sum.WorkerArtifactCount++
			viols := checkClaimsOnce(s, "worker/board-orientation ("+wr.ws.ID+")", bText, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}

		// RenderWorkerLegInject, RenderWorkerTaskAssignment, RenderWorkerTaskPreamble
		legVariants := workerLegContexts(wr.auth.Offered)
		for lIdx, lc := range legVariants {
			if lIdx > 0 && !firstOfReader[idx] {
				continue
			}
			legText, lErr := inject.RenderWorkerLegInject(ctx, injRenderer, wr.auth.Reader, "wsess", lc)
			if lErr != nil {
				return lErr
			}
			if legText != "" {
				sum.WorkerArtifactCount++
				viols := checkClaimsOnce(s, fmt.Sprintf("worker/leg[%d] (%s)", lIdx, wr.ws.ID), legText, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}

		if err := renderWorkerAssignmentAndPreamble(ctx, s, injRenderer, wr, firstOfReader[idx], sum); err != nil {
			return err
		}
	}
	return nil
}

func workerLegContexts(offered []string) []inject.WorkerLegContext {
	manyLeaves := make([]string, 260)
	for i := range manyLeaves {
		manyLeaves[i] = fmt.Sprintf("leaf-%d", i)
	}
	return []inject.WorkerLegContext{
		{
			LegID: "leg-1", AgentType: "implementer", WorkflowID: "implement", PhaseID: "execute",
			TopologyPattern: "fanout", RequiresIsolation: true,
			FailedLeaves:       manyLeaves,
			CompletionCriteria: []string{"Tests pass"},
			LegTools:           offered,
			Checklist:          []string{"Inspect target", "Verify"},
			ScanDigest:         []string{"0 critical findings"},
			HasPeerFindings:    true,
			SiblingNotesMore:   true,
			SiblingNotesAfter:  8,
			SiblingNotes: []inject.SiblingNote{
				{ID: 1, HasBody: true, Agent: "scout", Summary: "Found entrypoint", Ref: "finding#1"},
				{ID: 0, HasBody: false, Summary: "Quick note"},
			},
			ReservedPaths: []inject.ReservedPath{
				{Path: "pkg/a.go", Worker: "peer-a", LegLabel: "slice-a"},
				{Path: "pkg/b.go"},
			},
			LayoutTopLevel: []string{"cmd/", "internal/"},
			ReviewHandle:   "rev-1",
			ReviewWorker:   "impl-a",
			ViewKind:       "materialized",
		},
		{
			RequiresIsolation: false,
			ReviewHandle:      "rev-2",
			ViewKind:          "mounted",
		},
	}
}

func renderWorkerAssignmentAndPreamble(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	wr workerRep,
	firstOfReader bool,
	sum *CartesianSummary,
) error {
	mode := api.TaskScopeModeRead
	if wr.auth.Reader.Held()["command_mutation"] {
		mode = api.TaskScopeModeWrite
	}
	assignVariants := []map[string]any{
		{
			"scan_inventory": "2 scanners active", "coverage_assignment": `{"obligations":1}`, "coverage_required": true,
			"worker_name": "w-alpha", "round_instructions": "Focus on edge cases", "view_kind": "materialized",
			"scope_has_git": wr.ws.HasGit, "agent_type": wr.ws.AgentID, "worker_job_id": "job-1",
			"goal": "Complete assigned slice", "shared_context": "Repo uses Go 1.24",
			"known_facts": []string{"Fact 1"}, "constraints": []string{"Stay in scope"},
			"done_when": []string{"Verified"}, "context_refs": []string{"AGENTS.md"},
			"scope_mode": string(mode), "scope_paths": []string{"internal/a/**"}, "linked_checkout": true,
			"file_orientations": []map[string]any{
				{"path": "internal/a/a.go", "total_lines": 80, "size_bytes": 2048, "source": "ast", "symbols": []map[string]any{{"kind": "func", "name": "Run", "line": 10}, {"kind": "type", "name": "Cfg", "line": 4}}, "symbols_elided": 2},
				{"path": "internal/a/empty.go", "total_lines": 5, "size_bytes": 60, "source": "head"},
			},
			"attachments": []map[string]any{
				{"filename": "spec.txt", "mime": "text/plain", "path": "@attachment/spec.txt", "kind": "text", "hint": "text", "size_bytes": 128},
				{"filename": "note.txt", "mime": "text/plain", "path": "@attachment/note.txt", "kind": "text", "hint": "text"},
			},
			"recorded_verdicts": []map[string]any{{"phase": "claims", "evidence_key": "survey_claims", "fields": []map[string]any{{"name": "status", "value": "pass"}}}},
			"elisions":          []string{"1 symbol omitted"},
		},
		{
			"goal": "Read scope", "done_when": []string{"Summarized"}, "scope_mode": "read", "scope_has_git": true,
		},
		{
			"goal": "Minimal", "done_when": []string{"Done"},
		},
	}
	for aIdx, av := range assignVariants {
		if aIdx > 0 && !firstOfReader {
			continue
		}
		aText, err := anchor.RenderInformWithReader(ctx, anchor.InjectWorkerTaskAssignment, anchor.MatchContext{Surface: "worker", SessionID: "wsess"}, injRenderer, wr.auth.Reader, av)
		if err != nil {
			return err
		}
		if strings.TrimSpace(aText) != "" {
			sum.WorkerArtifactCount++
			viols := checkClaimsOnce(s, fmt.Sprintf("worker/task-assignment[%d] (%s)", aIdx, wr.ws.ID), aText, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}

	// RenderWorkerTaskPreamble (plus direct linked_checkout / scope_absent variants)
	pBlock, err := worker.RenderWorkerTaskPreamble(ctx, injRenderer, wr.auth.Reader, "wsess", "", []string{"internal/**"}, &api.TaskScope{Mode: mode, Paths: []string{"internal/a/**"}})
	if err != nil {
		return err
	}
	if strings.TrimSpace(pBlock) != "" {
		sum.WorkerArtifactCount++
		viols := checkClaimsOnce(s, "worker/task-preamble ("+wr.ws.ID+")", pBlock, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
		sum.Violations = append(sum.Violations, viols...)
	}
	if firstOfReader {
		for _, pv := range []map[string]any{
			{"touch_paths": []string{"internal/new/**"}, "scope_mode": "write", "scope_paths": []string{"internal/new/**"}, "scope_absent": true, "linked_checkout": true},
			{"scope_mode": "read"},
		} {
			rawP, pErr := injRenderer.RenderWithReader(ctx, wr.auth.Reader, "worker-task-preamble", pv)
			if pErr != nil {
				return pErr
			}
			sum.WorkerArtifactCount++
			viols := checkClaimsOnce(s, "worker/task-preamble-var ("+wr.ws.ID+")", rawP, wr.auth, wr.ws.ParentSessionMode, wr.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}
