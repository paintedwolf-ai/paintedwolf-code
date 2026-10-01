package main

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnreviewedOutputIsNeitherFalsePositiveNorPrecisionEvidence(t *testing.T) {
	var report scanReport
	testutil.FailErr(t, "decode findings", json.Unmarshal([]byte(`{"results":[
		{"path":"source/app.py","check_id":"rule","start":{"line":2,"col":3}},
		{"path":"source/app.py","check_id":"rule","start":{"line":2,"col":3}},
		{"path":"source/app.py","check_id":"rule","start":{"line":4,"col":1}}
	]}`), &report))
	project := projectCase{ReviewStatus: "pending", Findings: []projectFinding{
		{File: "app.py", Rule: "rule", Line: 2},
		{File: "app.py", Rule: "rule", Line: 6},
	}}
	m := projectMeasurement{Mode: opengrep.Intrafile, Run: 1, ReviewStatus: "pending", Families: map[string]familyCounts{}}
	classifyProjectFindings(&m, project, &report)
	if m.TruePositive != 1 || m.FalseNegative != 1 || m.Unresolved != 1 || m.FalsePositive != 0 || m.Duplicates != 1 {
		t.Fatalf("unreviewed classification: %+v", m)
	}
	if counts := m.Families["rule"]; counts.TruePositive != 1 || counts.FalseNegative != 1 || counts.Unresolved != 1 || counts.FalsePositive != 0 {
		t.Fatalf("family classification disagrees: %+v", counts)
	}
	summary := summarizeProjects(opengrep.Intrafile, []projectMeasurement{m})
	if summary.Precision != nil || summary.PrecisionLowerBound != nil || summary.PendingProjects != 1 {
		t.Fatalf("pending adjudication became precision evidence: %+v", summary)
	}
	m.Unresolved = 0
	summary = summarizeProjects(opengrep.Intrafile, []projectMeasurement{m})
	if summary.Precision != nil || summary.PrecisionLowerBound != nil {
		t.Fatal("a partially reviewed project acquired a precision claim after reviewing only its detections")
	}
	project.ReviewStatus = "complete"
	m = projectMeasurement{Families: map[string]familyCounts{}}
	classifyProjectFindings(&m, project, &report)
	if m.FalsePositive != 1 || m.Unresolved != 0 {
		t.Fatalf("reviewed unexpected result did not count as noise: %+v", m)
	}
}
