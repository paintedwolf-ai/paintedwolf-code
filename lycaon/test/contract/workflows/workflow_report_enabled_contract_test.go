package contract

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowSummaryReportEnabledOptIn(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)

	survey, ok := catalog["security-survey@1.0.1"]
	if !ok {
		t.Fatal("security-survey@1.0.1 missing from catalog")
	}
	if !survey.ReportEnabled() {
		t.Fatal("security-survey report is disabled")
	}
	if s := survey.Summary(); !s.ReportEnabled {
		t.Fatal("security-survey summary.report_enabled = false want true")
	}

	implement, ok := catalog["implement@1.0.0"]
	if !ok {
		t.Fatal("implement@1.0.0 missing from catalog")
	}
	if implement.ReportEnabled() {
		t.Fatal("implement report is enabled")
	}
	if s := implement.Summary(); s.ReportEnabled {
		t.Fatal("implement summary.report_enabled = true want false")
	}
}

func TestParseReportControlRoundTripAbsent(t *testing.T) {
	t.Parallel()
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: bare
version: 1.0.0
phases:
  - id: boot
    activity_label: Booting
`))
	contractcheck.FailErr(t, "ParseManifestYAML", err)
	if m.Summary().ReportEnabled {
		t.Fatal("bare summary.report_enabled want false")
	}
}
