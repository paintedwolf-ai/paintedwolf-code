package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnricher_DoesNotRenderSandboxCodes(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"SANDBOX_TRY_WRITE_ROOT": {Message: "reissue with capability_request.write_root"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "command",
		Output:    `{"exit_code":1,"network_mode":"proxy_only","boundary_refusal":"subject"}`,
		Facts:     ToolResultFacts{}.WithCode("SANDBOX_TRY_WRITE_ROOT"),
	}).Output
	if strings.Contains(out, "Code: SANDBOX_TRY_WRITE_ROOT") || strings.Contains(out, ">>> Tool feedback") {
		t.Fatalf("OAR renders SANDBOX banners; enricher must not render them:\n%s", out)
	}
}

func TestEnricher_RendersRaisedBannerFacts(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"BANNER_TASK_QUEUED": {
			Emit: "banner", What: "Worker {{ paintedwolf.job_id }} queued", Fix: "Wait for completion",
		},
	}}
	e := NewToolOutputEnricher(hints, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "task",
		Output:    `{"job_id":"job-1","status":"enqueued"}`,
		Facts: ToolResultFacts{}.WithFeedback(
			"BANNER_TASK_QUEUED", map[string]any{"job_id": "job-1"}, nil,
		),
	}).Output
	for _, want := range []string{"Worker job-1 queued", "Code: BANNER_TASK_QUEUED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("raised banner missing %q:\n%s", want, out)
		}
	}
}

func TestEnricher_FindOverflowNarrowBanner(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"FIND_OVERFLOW_NARROW": {Message: "narrow with name_glob"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	digest := `{"view":"digest","selected":0,"total":1225,"distribution":[{"kind":"ext","key":".md","count":500}],"truncated":true}`
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "find",
		Output:    "[find#1]\n" + digest,
	}).Output
	if !strings.Contains(out, "Code: FIND_OVERFLOW_NARROW") {
		t.Fatalf("missing find overflow banner:\n%s", out)
	}
	if !strings.Contains(out, "narrow with name_glob") {
		t.Fatalf("missing hint message:\n%s", out)
	}
}

func TestEnricher_FindOverflowNarrowOnWorkerChild(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"FIND_OVERFLOW_NARROW": {Message: "narrow with name_glob"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	parent := "parent-1"
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "child-1",
		Session:   &api.Session{ID: "child-1", ParentSessionID: parent},
		Tool:      "find",
		Output:    `{"view":"digest","selected":0,"truncated":true,"distribution":[{"kind":"dir","key":".","count":500}]}`,
	}).Output
	if !strings.Contains(out, "Code: FIND_OVERFLOW_NARROW") {
		t.Fatalf("worker child should get find overflow banner:\n%s", out)
	}
}

func TestEnricher_ProgressBannerDedupHash(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"SPEC_POSTURE_PROGRESS": {Message: "progress {{ paintedwolf.progress }} next {{ paintedwolf.next_action }}"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	in := EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "read",
		Output:    "ok",
		PlanProgress: PlanProgress{
			PhaseInferred:     1,
			PhaseInferredName: "stub",
			NextAction:        "write stub",
			ProgressChecklist: "[ ] Phase 1 — stub\n",
			ChecklistHash:     "h1",
		},
	}
	a := e.Enrich(t.Context(), in).Output
	b := e.Enrich(t.Context(), in).Output
	if !strings.Contains(a, "Code: SPEC_POSTURE_PROGRESS") {
		t.Fatalf("missing progress banner:\n%s", a)
	}
	if !strings.Contains(a, "next write stub") {
		t.Fatalf("missing rendered next action:\n%s", a)
	}
	if strings.Contains(b, "Code: SPEC_POSTURE_PROGRESS") {
		t.Fatalf("expected dedup omit progress banner:\n%s", b)
	}
}

func TestEnricher_BoardEmptyBanner(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"BOARD_EMPTY_SKIP_TO_VERIFY": {Message: "board empty — skip to verify"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "pack_board",
		Output:    `{"board_chars":0,"legs":[]}`,
	}).Output
	if !strings.Contains(out, "Code: BOARD_EMPTY_SKIP_TO_VERIFY") {
		t.Fatalf("missing board empty banner:\n%s", out)
	}
}

func TestEnricher_WorkerChildSkipsCoordinatorBanners(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"BOARD_EMPTY_SKIP_TO_VERIFY": {Message: "board empty — skip to verify"},
		"SPEC_POSTURE_PROGRESS":      {Message: "progress {{ paintedwolf.progress }} next {{ paintedwolf.next_action }}"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	parent := "parent-1"
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "child-1",
		Session:   &api.Session{ID: "child-1", ParentSessionID: parent, Posture: api.SessionPostureSpec},
		Tool:      "pack_board",
		Output:    `{"board_chars":0,"legs":[]}`,
		PlanProgress: PlanProgress{
			PhaseInferred:     1,
			PhaseInferredName: "stub",
			NextAction:        "write stub",
			ProgressChecklist: "[ ] Phase 1 — stub\n",
			ChecklistHash:     "h1",
		},
	}).Output
	if strings.Contains(out, "Code: BOARD_EMPTY_SKIP_TO_VERIFY") {
		t.Fatalf("worker child should not get coordinator banners:\n%s", out)
	}
	if strings.Contains(out, "Code: SPEC_POSTURE_PROGRESS") {
		t.Fatalf("worker child should not get spec progress banner:\n%s", out)
	}
}

func TestEnricher_EmptyOutputNoBanners(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{}, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "read",
		Output:    "",
	}).Output
	if strings.Contains(out, ">>> Tool feedback") {
		t.Fatalf("empty output should stay empty, got:\n%s", out)
	}
}

func TestEnricher_FeedbackPendingNoImperativeDup(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{}}
	e := NewToolOutputEnricher(hints, nil)
	in := EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "read",
		Args:      map[string]any{"_pending_feedback_line": "pending_feedback: phase `plan`"},
		Output:    "ok",
	}
	out := e.Enrich(t.Context(), in).Output
	if !strings.Contains(out, "pending_feedback: phase `plan`") {
		t.Fatalf("missing pending feedback line:\n%s", out)
	}
	// Repeat should not duplicate line.
	out2 := e.Enrich(t.Context(), in).Output
	if strings.Count(out2, "pending_feedback:") != 1 {
		t.Fatalf("expected no duplication:\n%s", out2)
	}
}

func TestEnricher_ReviewLoopTaskCompletes(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_GATE_BLOCKED": {Message: "fallback blocked at {{ paintedwolf.phase }}"},
	}}
	e := NewToolOutputEnricher(hints, catalog)
	got := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "task",
		Output:    `{"agent_type":"skeptic","job_id":"job-1","status":"enqueued"}`,
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "security-survey", CurrentPhase: "challenge",
			FailedLeaves: []string{"evidence_passed:survey_challenged"}, RunActive: true,
		},
	})
	if strings.Contains(got.Output, "Code: WORKFLOW_GATE_BLOCKED") {
		t.Fatalf("unexpected WORKFLOW_GATE_BLOCKED:\n%s", got.Output)
	}
	if got.Facts.Resolution() != api.ToolResultOutcomeCompleted {
		t.Fatalf("resolution = %q want completed", got.Facts.Resolution())
	}
}

func TestEnricher_HostHITLToolCompletes(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_GATE_BLOCKED": {Message: "fallback blocked at {{ paintedwolf.phase }}"},
	}}
	e := NewToolOutputEnricher(hints, catalog)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "write",
		Output:    "ok",
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"}, RunActive: true,
		},
	}).Output
	if strings.Contains(out, "Code: WORKFLOW_GATE_BLOCKED") {
		t.Fatalf("unexpected WORKFLOW_GATE_BLOCKED:\n%s", out)
	}
	if strings.Contains(out, "blocked=human_approval") {
		t.Fatalf("unexpected blocked=human_approval:\n%s", out)
	}
}

func TestEnricher_WorkflowAdvanceBlocksOnHumanApproval(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_GATE_BLOCKED": {Message: "fallback blocked at {{ paintedwolf.phase }}"},
	}}
	e := NewToolOutputEnricher(hints, catalog)
	got := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "workflow_advance",
		Output:    "ok",
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"}, RunActive: true,
		},
	})
	out := got.Output
	if !strings.Contains(out, "Progress: phase=approve; blocked=human_approval") {
		t.Fatalf("missing workflow progress line:\n%s", out)
	}
	if !strings.Contains(out, "Gate blocked: human_approval") {
		t.Fatalf("missing structured gate feedback:\n%s", out)
	}
	if !strings.Contains(out, "To satisfy:") {
		t.Fatalf("missing satisfy steps:\n%s", out)
	}
	if !strings.Contains(out, "Code: WORKFLOW_GATE_BLOCKED") {
		t.Fatalf("missing gate blocked banner code:\n%s", out)
	}
	if got.Facts.Resolution() != api.ToolResultOutcomeRejected {
		t.Fatalf("resolution = %q want rejected", got.Facts.Resolution())
	}
}

func TestEnricher_FanoutPlannedSurveyToolsComplete(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_GATE_BLOCKED": {Message: "fallback blocked at {{ paintedwolf.phase }}"},
	}}
	e := NewToolOutputEnricher(hints, catalog)
	for _, tool := range []string{"survey_repo", "summarize", "read", "grep"} {
		got := e.Enrich(t.Context(), EnrichInput{
			SessionID: "s1",
			Session:   &api.Session{ID: "s1"},
			Tool:      tool,
			Output:    `{"ok":true,"completeness":"complete"}`,
			Workflow: feedback.WorkflowEvaluationContext{
				WorkflowID: "recon-pack", CurrentPhase: "plan",
				FailedLeaves: []string{"fanout_planned"}, RunActive: true,
			},
		})
		if !strings.Contains(got.Output, "Code: WORKFLOW_GATE_BLOCKED") {
			t.Fatalf("%s: missing gate banner:\n%s", tool, got.Output)
		}
		if got.Facts.Resolution() != api.ToolResultOutcomeCompleted {
			t.Fatalf("%s: resolution = %q want completed", tool, got.Facts.Resolution())
		}
	}
}

func TestEnricher_WorkflowGateStructuredFeedback(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_GATE_BLOCKED": {Message: "fallback blocked at {{ paintedwolf.phase }}"},
	}}
	e := NewToolOutputEnricher(hints, catalog)
	got := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "write",
		Output:    "ok",
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "expand", FailedLeaves: []string{"plan_stub_valid"}, RunActive: true,
		},
	})
	out := got.Output
	if !strings.Contains(out, "Progress: phase=expand; blocked=plan_stub_valid") {
		t.Fatalf("missing workflow progress line:\n%s", out)
	}
	if !strings.Contains(out, "Gate blocked: plan_stub_valid") {
		t.Fatalf("missing structured gate feedback:\n%s", out)
	}
	if !strings.Contains(out, "To satisfy:") {
		t.Fatalf("missing satisfy steps:\n%s", out)
	}
	if !strings.Contains(out, "Code: WORKFLOW_GATE_BLOCKED") {
		t.Fatalf("missing gate blocked banner code:\n%s", out)
	}
	if got.Facts.Resolution() != api.ToolResultOutcomeCompleted {
		t.Fatalf("resolution = %q want completed", got.Facts.Resolution())
	}
}

func TestEnricher_WorkflowPhaseExitRequired(t *testing.T) {
	hints := &HintConfig{HintCodes: map[string]HintEntry{
		"WORKFLOW_PHASE_EXIT_REQUIRED": {Message: "phase {{ paintedwolf.phase }} — call workflow_advance now"},
	}}
	e := NewToolOutputEnricher(hints, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
		Tool:      "read",
		Args:      map[string]any{"path": "blueprint.md"},
		Output:    `{"mode":"outline","total_lines":2500,"survey_recommended":true}`,
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID:         "example",
			CurrentPhase:       "intake",
			RunActive:          true,
			AdvanceWhenGateMet: "coordinator",
			CurrentGatesKnown:  true,
			CurrentGatesPassed: true,
			PhaseExitKind:      "proof",
		},
	}).Output
	if !strings.Contains(out, "Code: WORKFLOW_PHASE_EXIT_REQUIRED") ||
		!strings.Contains(out, "workflow_advance") {
		t.Fatalf("missing phase-exit recovery:\n%s", out)
	}
}

func TestEnricherBoardRequiresExplicitMeasurement(t *testing.T) {
	for _, raw := range []string{`{}`, `{"board_chars":null}`, `{"roster":{"running":[{"job_id":"job-1"}]}}`, `{"board_chars":1}`} {
		if boardIsEmpty(raw) {
			t.Fatalf("unmeasured/nonempty view treated as empty: %s", raw)
		}
	}
}

func TestEnricherProgressChangesWithUnchangedChecklist(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{HintCodes: map[string]HintEntry{"SPEC_POSTURE_PROGRESS": {Message: "{{ paintedwolf.phase }} {{ paintedwolf.next_action }}"}}}, nil)
	in := EnrichInput{SessionID: "s1", Session: &api.Session{ID: "s1", Posture: api.SessionPostureSpec}, Tool: "read", Output: "ok", PlanProgress: PlanProgress{ChecklistHash: "same", PhaseInferred: 1, NextAction: "review"}}
	e.Enrich(t.Context(), in)
	in.PlanProgress.PhaseInferred = 2
	in.PlanProgress.NextAction = "approve"
	if out := e.Enrich(t.Context(), in); !strings.Contains(out.Output, "Code: SPEC_POSTURE_PROGRESS") {
		t.Fatalf("changed planning state suppressed: %+v", out)
	}
}

func TestEnricherBannerKeepsTunedRecovery(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{HintCodes: map[string]HintEntry{"FIXTURE": {What: "state", Cause: "cause", Why: "reason", Fix: "recover {{ paintedwolf.actual }}", Instead: "next"}}}, nil)
	text := e.bannerMessage("FIXTURE", map[string]any{"actual": "field"})
	for _, want := range []string{"state", "cause", "reason", "recover field", "next"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost %q: %s", want, text)
		}
	}
}
