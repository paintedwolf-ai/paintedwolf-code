package worker

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatHunkSummaryLineIncludesLineRange(t *testing.T) {
	line := formatHunkSummaryLine(api.WorkerMergeHunk{
		StartLine: 12,
		EndLine:   18,
		Primary:   "primary text",
		Branch:    "branch text",
	})
	if !strings.HasPrefix(line, "L12-18:") {
		t.Fatalf("line = %q want L12-18 prefix", line)
	}
	if !strings.Contains(line, "primary:") || !strings.Contains(line, "branch:") {
		t.Fatalf("line = %q want both sides", line)
	}
}
