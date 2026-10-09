package feedback_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// A run on a sealed version reads that version's gate feedback; live runs and
// gates the archive does not seal keep the current definitions.
func TestSealedVersionReadsItsOwnGateFeedback(t *testing.T) {
	eff := extpackstest.StockCatalog(t)
	live, err := feedback.LoadGateFeedbackCatalogWithCatalog(eff)
	testutil.FailErr(t, "load gate feedback", err)
	sealed := live.WithWorkflowArchive("security-survey/1.0.0")
	if live.WithWorkflowArchive("") != live || live.WithWorkflowArchive("unknown/9.9.9") != live {
		t.Fatal("a version without sealed feedback must read the live catalog")
	}

	archived, _, ok := eff.UnitContent(extpacks.ArchiveGuidanceUnitID("security-survey/1.0.0", "gate-feedback/evidence_passed-survey_challenged"))
	if !ok {
		t.Fatal("stock catalog does not seal the challenge gate feedback")
	}
	var want feedback.GateFeedbackDef
	testutil.FailErr(t, "decode sealed feedback", config.DecodeYAML(archived, &want))
	got, _ := sealed.Def("evidence_passed:survey_challenged")
	current, _ := live.Def("evidence_passed:survey_challenged")
	if !reflect.DeepEqual(got, want) || reflect.DeepEqual(current, want) {
		t.Fatalf("sealed run read %+v, want the sealed definition %+v (live %+v)", got, want, current)
	}
	if !sealed.Has("worker_cycle_ready") {
		t.Fatal("gates the archive does not seal must stay resolvable")
	}
}

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
