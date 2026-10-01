package feedback_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadGateFeedbackCatalog(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	if !catalog.Has("recon_or_board_ready") {
		t.Fatal("expected recon_or_board_ready feedback")
	}
	out, err := catalog.RenderGateFeedback(t.Context(), "worker_cycle_ready", map[string]any{})
	testutil.FailErr(t, "RenderGateFeedback", err)
	if !strings.Contains(out, "worker_cycle_ready") {
		t.Fatalf("render missing gate id: %q", out)
	}
}

func TestProjectObligationsCompactSatisfy(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	rows := catalog.ProjectObligations(t.Context(), []string{"research_satisfied", "missing-leaf"}, "coordinator", nil)
	if len(rows) != 1 || rows[0].ID != "research_satisfied" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Purpose == "" || !strings.Contains(strings.Join(rows[0].Satisfy, "\n"), "research") {
		t.Fatalf("obligation = %+v", rows[0])
	}
	if strings.Contains(rows[0].Purpose, "\n") {
		t.Fatalf("purpose must be compact one-line: %q", rows[0].Purpose)
	}
}

func TestPlanStubGateFeedbackListsMissingHeadings(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	body := "## Scope\n\nBuild a CLI.\n\n## API design\n\nx\n"
	extras := feedback.PlanStubGateExtras(body)
	ctx := feedback.GateFeedbackContext(feedback.WorkflowEvaluationContext{
		AdvanceWhenGateMet: "auto",
		CurrentPhase:       "expand",
	}, extras)
	out, err := catalog.RenderGateFeedback(t.Context(), "plan_stub_valid", ctx)
	testutil.FailErr(t, "RenderGateFeedback", err)
	if !strings.Contains(out, "## Goal") || !strings.Contains(out, "Headings present") {
		t.Fatalf("expected missing/present diagnostics:\n%s", out)
	}
	if !strings.Contains(out, "## Scope") {
		t.Fatalf("expected present heading ## Scope:\n%s", out)
	}
	rows := catalog.ProjectObligations(t.Context(), []string{"plan_stub_valid"}, "auto", extras)
	if len(rows) != 1 || len(rows[0].Required) < 6 {
		t.Fatalf("expected required headings on obligation: %+v", rows)
	}
	if !strings.Contains(rows[0].Required[0], "## Goal") {
		t.Fatalf("required[0]=%q", rows[0].Required[0])
	}
}

func TestProjectObligationsRendersReviewRosterInSatisfy(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	extras := feedback.WithReviewAgents(nil, []string{"skeptic", "web-researcher"})
	rows := catalog.ProjectObligations(t.Context(), []string{"evidence_passed:survey_challenged"}, "", extras)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
	joined := strings.Join(rows[0].Satisfy, " ")
	if !strings.Contains(joined, "`skeptic`, `web-researcher`") {
		t.Fatalf("satisfy must name the owed reviewers: %v", rows[0].Satisfy)
	}
}

func TestProjectObligationsSatisfyOmitsRosterClauseWithoutExtras(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	rows := catalog.ProjectObligations(t.Context(), []string{"evidence_passed:survey_challenged"}, "", nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
	joined := strings.Join(rows[0].Satisfy, " ")
	if strings.Contains(joined, "{{") || strings.Contains(joined, "{%") {
		t.Fatalf("satisfy must render template syntax away: %v", rows[0].Satisfy)
	}
	if !strings.Contains(joined, "owed reviewer") {
		t.Fatalf("satisfy must keep the reviewer instruction: %v", rows[0].Satisfy)
	}
}
