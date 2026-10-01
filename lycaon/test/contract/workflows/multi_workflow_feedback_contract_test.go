package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFeedbackPackageNoBlueprintImport(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "guidance", "feedback")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		imports, err := goFileImports(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, imp := range imports {
			if strings.Contains(imp, "/internal/blueprint") {
				t.Fatalf("%s imports blueprint package %q", path, imp)
			}
		}
	}
}

func TestWorkflowGateBlockedHintRegistered(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	if _, ok := hintCfg.HintCodes["WORKFLOW_GATE_BLOCKED"]; !ok {
		t.Fatal("expected WORKFLOW_GATE_BLOCKED in hint registry")
	}
}

func TestMultiWorkflowEnrichCitesPhaseAndStructuredGateFeedback(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	enricher := guidance.NewToolOutputEnricher(hintCfg, gateCfg)

	t.Run("plan_approve_human_approval_read_completes", func(t *testing.T) {
		t.Parallel()
		out := enricher.Enrich(t.Context(), guidance.EnrichInput{
			SessionID: "s-plan",
			Session:   &api.Session{ID: "s-plan", Posture: api.SessionPostureSpec},
			Tool:      "read",
			Output:    `{"ok":true}`,
			Workflow: feedback.WorkflowEvaluationContext{
				WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"}, RunActive: true,
			},
		}).Output
		if strings.Contains(out, "WORKFLOW_GATE_BLOCKED") || strings.Contains(out, "blocked=human_approval") {
			t.Fatalf("unexpected WORKFLOW_GATE_BLOCKED:\n%s", out)
		}
	})

	t.Run("plan_approve_human_approval_blocks_advance", func(t *testing.T) {
		t.Parallel()
		out := enricher.Enrich(t.Context(), guidance.EnrichInput{
			SessionID: "s-plan-adv",
			Session:   &api.Session{ID: "s-plan-adv", Posture: api.SessionPostureSpec},
			Tool:      "workflow_advance",
			Output:    `{"ok":true}`,
			Workflow: feedback.WorkflowEvaluationContext{
				WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"}, RunActive: true,
			},
		}).Output
		assertContainsAll(t, out, "phase=approve", "blocked=human_approval", "Gate blocked: human_approval", "WORKFLOW_GATE_BLOCKED")
	})

	t.Run("bugbash_parallel_stage_feedback", func(t *testing.T) {
		t.Parallel()
		got := enricher.Enrich(t.Context(), guidance.EnrichInput{
			SessionID: "s-pipe",
			Session:   &api.Session{ID: "s-pipe", Posture: api.SessionPostureOrchestrate},
			Tool:      "read",
			Output:    `{"ok":true}`,
			Workflow: feedback.WorkflowEvaluationContext{
				WorkflowID: "bugbash", CurrentPhase: "hunt",
				FailedLeaves: []string{"parallel_stages_complete"}, RunActive: true,
			},
		})
		assertContainsAll(t, got.Output, "phase=hunt", "blocked=parallel_stages_complete", "Workflow gate blocked", "WORKFLOW_GATE_BLOCKED")
		if got.Facts.Resolution() != api.ToolResultOutcomeCompleted {
			t.Fatalf("resolution = %q want completed", got.Facts.Resolution())
		}
	})

	t.Run("recon_fanout_planned_survey_completes", func(t *testing.T) {
		t.Parallel()
		for _, tool := range []string{"survey_repo", "summarize"} {
			got := enricher.Enrich(t.Context(), guidance.EnrichInput{
				SessionID: "s-recon",
				Session:   &api.Session{ID: "s-recon", Posture: api.SessionPostureOrchestrate},
				Tool:      tool,
				Output:    `{"ok":true,"completeness":"complete"}`,
				Workflow: feedback.WorkflowEvaluationContext{
					WorkflowID: "recon-pack", CurrentPhase: "plan",
					FailedLeaves: []string{"fanout_planned"}, RunActive: true,
				},
			})
			assertContainsAll(t, got.Output, "WORKFLOW_GATE_BLOCKED")
			if got.Facts.Resolution() != api.ToolResultOutcomeCompleted {
				t.Fatalf("%s: resolution = %q want completed", tool, got.Facts.Resolution())
			}
		}
	})
}

func assertContainsAll(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Fatalf("output missing %q:\n%s", n, haystack)
		}
	}
}
