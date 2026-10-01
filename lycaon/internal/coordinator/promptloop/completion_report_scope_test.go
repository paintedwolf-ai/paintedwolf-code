package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentFieldsRequireRunReportBinding(t *testing.T) {
	report := guidance.CoordinatorCompletionReport{
		Headline: "Conclusion", Summary: "Assessment", Limits: []string{"Unchecked"},
		Findings: []guidance.CoordinatorFinding{{Title: "Finding"}},
	}
	for _, binding := range []CompletionReportBinding{{}, {RunID: "plan", Phase: "research"}} {
		meta := completionReportMeta("implement_synthesis", binding, report)
		if meta.Scope == api.CompletionReportScopeRun || meta.Headline != "" || meta.Summary != "" || len(meta.Findings) != 0 || len(meta.Limits) != 0 {
			t.Fatalf("ordinary completion retained document fields: %+v", meta)
		}
	}
	meta := completionReportMeta("implement_synthesis", CompletionReportBinding{RunID: "survey", Phase: "report", PhaseDeliversRunReport: true}, report)
	if meta.Scope != api.CompletionReportScopeRun || meta.Headline == "" || len(meta.Findings) != 1 {
		t.Fatalf("enabled report lost document fields: %+v", meta)
	}
}

func testReportFrame() inject.CoordinatorTurnFrame {
	return inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{RunID: "report-run", CurrentPhase: "report"},
		Runtime:    inject.WorkflowRuntimeSnapshot{ReportDocumentEnabled: true},
	}
}
