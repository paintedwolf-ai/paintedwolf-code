package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func surveyLedger(t *testing.T, path string) evidence.Ledger {
	t.Helper()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "r1", Args: map[string]any{"path": path}},
		}},
		{Role: api.MessageRoleTool, Content: `{"path":"` + path + `","mode":"outline","symbols":[{"kind":"func","name":"main","line":1}],"total_lines":500,"truncated":false,"selected":0,"total":1}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	surveyed := false
	for _, rec := range ev.Handles {
		if rec.Survey {
			surveyed = true
		}
	}
	if !surveyed {
		t.Fatalf("fixture produced no survey-grade record for %q", path)
	}
	return ev
}

// Survey citations record an advisory without blocking closeout.
func TestEvaluateCloseoutCitations_surveyCitationIsAdvisory(t *testing.T) {
	ev := guidance.CloseoutEvidence{Ledger: surveyLedger(t, "pkg/a.go")}
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "Surveyed pkg/a.go",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "pkg/a.go"}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if strings.TrimSpace(eval.Code) != "" {
		t.Fatalf("code=%q want empty (survey citation is advisory, not blocking)", eval.Code)
	}
	if eval.SurveyAdvisoryCount != 1 {
		t.Fatalf("survey advisory count=%d want 1; tokens=%v", eval.SurveyAdvisoryCount, eval.SurveyAdvisoryTokens)
	}
}

// Survey provenance remains visible on the citation.
func TestBuildCloseoutCitationGrounding_advisoriesOnEnvelope(t *testing.T) {
	ev := guidance.CloseoutEvidence{Ledger: surveyLedger(t, "pkg/a.go")}
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "Surveyed pkg/a.go; see docs",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "pkg/a.go"}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	g := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev, eval)
	if g == nil {
		t.Fatal("expected a grounding envelope")
	}
	if !g.Traced {
		t.Fatalf("grounding traced=false; advisory reclassification must still land: %+v", g)
	}
	statuses := map[string]api.CitationGroundingCheckStatus{}
	for _, c := range g.Checks {
		statuses[c.ID] = c.Status
	}
	if statuses["survey_advisory"] != api.CitationGroundingCheckStatusAdvisory {
		t.Fatalf("survey advisory check missing from envelope: %+v", g.Checks)
	}
}
