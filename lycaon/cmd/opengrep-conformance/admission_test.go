package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

func TestAdmissionDoesNotTreatFindingsInOneProjectAsIndependentEvidence(t *testing.T) {
	summary := summarizeProjects(opengrep.Intrafile, []projectMeasurement{{Mode: opengrep.Intrafile, Run: 1, TruePositive: 500}})
	if summary.FindingProjects != 1 || summary.PrecisionLowerBound == nil || *summary.PrecisionLowerBound > 0.3 {
		t.Fatalf("one project's findings inflated uncertainty bound: %+v", summary)
	}
}

func TestAdmissionCannotHideSparseFamilyBehindLargeFamily(t *testing.T) {
	policy := &admissionPolicy{MinimumTruePositives: 2, MinimumProjectPrecisionLowerBound: 0.1, MaximumP95Milliseconds: 100, MaximumPeakRSSBytes: 1000, MaximumOutputBytes: 1000}
	measurement := projectMeasurement{Mode: opengrep.Intrafile, Run: 1, TruePositive: 41, MemorySamples: 1, PeakRSSBytes: 10, Families: map[string]familyCounts{"broad": {TruePositive: 40}, "sparse": {TruePositive: 1}}}
	var output bytes.Buffer
	if err := writeFamilySummaries(json.NewEncoder(&output), []projectMeasurement{measurement}, opengrep.Intrafile, policy, "held_out"); err == nil {
		t.Fatal("sparse family was admitted by aggregate evidence")
	}
}

func TestAdmissionDoesNotCountRepetitionsAsEvidence(t *testing.T) {
	var runs []projectMeasurement
	for run := 1; run <= 10; run++ {
		runs = append(runs, projectMeasurement{Mode: opengrep.Intrafile, Run: run, TruePositive: 4, Milliseconds: 100, MemorySamples: 1, PeakRSSBytes: 1000, OutputBytes: 100})
	}
	summary := summarizeProjects(opengrep.Intrafile, runs)
	if summary.TruePositive != 4 || summary.Projects != 1 {
		t.Fatalf("repetitions inflated evidence: %+v", summary)
	}
	policy := admissionPolicy{MinimumTruePositives: 35, MinimumProjectPrecisionLowerBound: 0.9, MaximumP95Milliseconds: 1000, MaximumPeakRSSBytes: 10000, MaximumOutputBytes: 1000}
	if len(policy.reject(summary)) == 0 {
		t.Fatal("four observations were sufficient for admission")
	}
	summary.TruePositive = 40
	precision, lower := 1.0, wilsonLower(40, 40)
	summary.Precision = &precision
	summary.PrecisionLowerBound = &lower
	if reasons := policy.reject(summary); len(reasons) != 0 {
		t.Fatalf("bounded perfect sample rejected: %v", reasons)
	}
	summary.FalsePositive = 1
	if len(policy.reject(summary)) == 0 {
		t.Fatal("known false positive was admitted")
	}
}

func TestAdmissionRejectsUnmeasuredOrFailedScans(t *testing.T) {
	policy := admissionPolicy{MinimumTruePositives: 1, MinimumProjectPrecisionLowerBound: 0.5, MaximumP95Milliseconds: 100, MaximumPeakRSSBytes: 1000, MaximumOutputBytes: 1000}
	summary := evaluationSummary{Failures: 1}
	if len(policy.reject(summary)) < 3 {
		t.Fatal("missing evidence or measurement passed")
	}
	if (admissionPolicy{}).validate() == nil {
		t.Fatal("zero-value policy accepted")
	}
}

func TestAdmissionRejectsAnyUnmeasuredRepetition(t *testing.T) {
	runs := []projectMeasurement{
		{Mode: opengrep.Intrafile, Run: 1, MemorySamples: 5, PeakRSSBytes: 100},
		{Mode: opengrep.Intrafile, Run: 2},
		{Mode: opengrep.Intrafile, Run: 3, MemorySamples: 5, PeakRSSBytes: 100, MemoryFailure: "worker observation failed"},
	}
	summary := summarizeProjects(opengrep.Intrafile, runs)
	if summary.UnmeasuredRuns != 2 {
		t.Fatalf("missing observations hidden: %+v", summary)
	}
	if !slices.Contains((admissionPolicy{}).reject(summary), "peak memory measurement unavailable") {
		t.Fatal("partial memory measurements admitted")
	}
}
