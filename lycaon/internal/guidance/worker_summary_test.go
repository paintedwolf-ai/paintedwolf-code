package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFormatWorkerSummaryFeedback(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	out, err := guidance.FormatWorkerSummaryFeedback(hints, "WORKER_SUMMARY_NO_ARTIFACT", nil)
	testutil.FailErr(t, "guidance.FormatWorkerSummaryFeedback failed", err)
	if !strings.Contains(out, "WORKER_SUMMARY_NO_ARTIFACT") {
		t.Fatalf("out = %q", out)
	}
}

func TestFormatWorkerSummaryFeedbackNamesOffenders(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	data := map[string]any{
		"offender_count":        1,
		"offenders_sample":      "read#9",
		"offenders_omitted":     0,
		"observed_paths_sample": "pkg/main.go",
	}
	out, err := guidance.FormatWorkerSummaryFeedback(hints, "WORKER_EVIDENCE_HANDLE_UNKNOWN", data)
	testutil.FailErr(t, "guidance.FormatWorkerSummaryFeedback failed", err)
	if !strings.Contains(out, "read#9") {
		t.Fatalf("reject must name offending handles, got %q", out)
	}
	if !strings.Contains(out, "WORKER_EVIDENCE_HANDLE_UNKNOWN") {
		t.Fatalf("reject must include code, got %q", out)
	}
}
