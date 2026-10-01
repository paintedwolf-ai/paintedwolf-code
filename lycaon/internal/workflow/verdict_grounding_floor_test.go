package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseVerdictClaims(t *testing.T) {
	t.Parallel()
	def := workflowdef.ReviewLoopDef{
		EvidenceKey: "survey_claims",
		VerdictSchema: map[string]string{
			"verdict": "CLAIMED",
			"claims":  "claims",
		},
	}
	verdict := map[string]string{
		"verdict": "CLAIMED",
		"claims":  `[{"id":"c1","statement":"SQLi","cited_evidence":[{"path":"a.go","line":8}]}]`,
	}
	claims, err := ParseVerdictClaims(def, verdict)
	testutil.FailErr(t, "parse claims", err)
	list := claims["claims"]
	if len(list) != 1 || list[0].ID != "c1" || len(list[0].CitedEvidence) != 1 {
		t.Fatalf("claims = %+v", list)
	}

	for name, bad := range map[string]string{
		"not json":            "prose, not a list",
		"missing id":          `[{"statement":"x"}]`,
		"duplicate id":        `[{"id":"c1","statement":"x"},{"id":"c1","statement":"y"}]`,
		"empty citation":      `[{"id":"c1","statement":"x","cited_evidence":[{"line":3}]}]`,
		"unknown claim field": `[{"id":"c1","statement":"x","evidence":"read#1"}]`,
		"evidence field name": `[{"id":"c1","statement":"x","cited_evidence":[{"evidence":"read#1"}]}]`,
		"trailing JSON":       `[{"id":"c1","statement":"x"}] {}`,
	} {
		verdict["claims"] = bad
		if _, err := ParseVerdictClaims(def, verdict); err == nil {
			t.Fatalf("%s: expected parse error", name)
		}
	}
}

func TestRecordReviewLoopVerdictGroundingFloor(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	startReviewLoopRun(ctx, t, mgr)

	terminal := map[string]string{"verdict": "SELECTED", "winner": "B"}
	cited := []api.CitationGroundingCitedEvidence{{Handle: "leg-1:read#1"}}

	// Audit reject holds the gate open without consuming an attempt as terminal.
	mgr.VerdictGrounding = func(_ context.Context, _ string, got []api.CitationGroundingCitedEvidence, urls, _ []string) (guidance.VerdictGroundingEval, error) {
		if len(got) != 1 || got[0].Handle != "leg-1:read#1" || len(urls) != 1 {
			t.Fatalf("audit inputs = %+v urls=%v", got, urls)
		}
		return guidance.VerdictGroundingEval{
			Code:             guidance.VerdictReviewerUncitedCode,
			UncitedReviewers: []string{"skeptic"},
		}, nil
	}
	out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", terminal, cited, []string{"https://example.com"})
	testutil.FailErr(t, "rejected verdict", err)
	if out.Valid || out.Terminal {
		t.Fatalf("outcome = %+v want invalid, non-terminal", out)
	}
	if out.GroundingCode != guidance.VerdictReviewerUncitedCode || len(out.UncitedReviewers) != 1 {
		t.Fatalf("grounding reject = %q uncited=%v", out.GroundingCode, out.UncitedReviewers)
	}

	// A grounded audit stamps provenance and the verdict advances.
	granted := &api.CitationGrounding{Traced: true}
	mgr.VerdictGrounding = func(context.Context, string, []api.CitationGroundingCitedEvidence, []string, []string) (guidance.VerdictGroundingEval, error) {
		return guidance.VerdictGroundingEval{Grounding: granted}, nil
	}
	out, err = mgr.RecordReviewLoopVerdict(ctx, "sess-1", terminal, cited, nil)
	testutil.FailErr(t, "grounded verdict", err)
	if !out.Valid || !out.Terminal || out.Grounding != granted {
		t.Fatalf("outcome = %+v want terminal with grounding", out)
	}
}
