package packboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildInjectLinesPulseOmitsStableSlices(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	completed := now.Add(-4 * time.Minute)
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 5},
		Git:  &api.BoardGitSlice{Available: false},
		Scans: &api.BoardScansSlice{
			CurrentAssessment: &api.BoardScanSummary{
				AssessmentID:  "scan-1",
				Status:        api.CodeScanStatusComplete,
				Categories:    []api.ScanCategory{api.ScanCategorySecurity},
				FindingsCount: 0,
				CreatedAt:     completed,
				CompletedAt:   &completed,
			},
		},
	}
	full := packboard.BuildInjectLines(snap, packboard.InjectScopeFull, packboard.OrientOpts{Now: now})
	pulse := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: now})
	fullText := strings.Join(full, "\n")
	pulseText := strings.Join(pulse, "\n")
	if !strings.Contains(fullText, "Repo:") || !strings.Contains(fullText, packboard.GitNoneOrientationLine) {
		t.Fatalf("full = %q", fullText)
	}
	if strings.Contains(pulseText, "Repo:") || strings.Contains(pulseText, "Git:") {
		t.Fatalf("pulse should omit stable slices: %q", pulseText)
	}
	if !strings.HasPrefix(pulseText, "Now:") {
		t.Fatalf("pulse = %q", pulseText)
	}
	for _, text := range []string{fullText, pulseText} {
		if !strings.Contains(text, "Scan:") || !strings.Contains(text, "clean") {
			t.Fatalf("inject should include Scan line with clean: %q", text)
		}
	}
}

func TestBuildInjectLinesIncludesScanRegressionTail(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Minute)
	snap := api.BoardSnapshot{
		Scans: &api.BoardScansSlice{
			CurrentAssessment: &api.BoardScanSummary{
				AssessmentID:  "scan-2",
				Status:        api.CodeScanStatusComplete,
				Categories:    []api.ScanCategory{api.ScanCategorySecurity},
				FindingsCount: 5,
				CreatedAt:     completed,
				CompletedAt:   &completed,
			},
			Compare: &api.BoardScanCompareSlice{
				NewCount: 2,
				NewByLevel: map[string]int{
					string(api.FindingLevelMedium): 2,
				},
			},
		},
	}
	lines := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: now})
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "+2 new") {
		t.Fatalf("pulse inject = %q", body)
	}
}

func TestInjectSentinel(t *testing.T) {
	if packboard.InjectSentinel(packboard.InjectScopeFull) != packboard.PackBoardSentinel {
		t.Fatal("full sentinel mismatch")
	}
	if packboard.InjectSentinel(packboard.InjectScopePulse) != packboard.PackBoardPulseSentinel {
		t.Fatal("pulse sentinel mismatch")
	}
}

func TestInjectedBoardRetainsPendingWorkerIntegration(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	snapshot := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{
		"tasks":              []api.WorkerTask{{ID: "abcd-worker", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending}},
		"overlay_merge_plan": &api.OverlayMergePlan{PendingCount: 1, PromoteSequence: []string{"abcd-worker"}},
		"promote_paths":      []api.WorkerPromoteJobPathStatus{{WorkerID: "abcd-worker", Paths: []api.WorkerPromotePathStatus{{Path: "status.py", Status: api.WorkerPromotePathOutcomeConflict, HunkCount: 1}}}},
	}}
	for _, scope := range []packboard.InjectScope{packboard.InjectScopeFull, packboard.InjectScopePulse} {
		lines, truncated := packboard.FormatInjectBodyScoped(snapshot, scope, false, now, api.MaxBoardInjectChars)
		body := strings.Join(lines, "\n")
		if truncated {
			t.Fatalf("small worker state was truncated: %s", body)
		}
		for _, want := range []string{"Work: 1 unreported", "Merge: pending abcd", "Overlay plan: 1 pending", "status.py:conflict(1)"} {
			if !strings.Contains(body, want) {
				t.Errorf("scope %v omitted %q: %s", scope, want, body)
			}
		}
		for _, line := range packboard.BuildOrientationLines(snapshot, packboard.OrientOpts{Now: now}) {
			if strings.HasPrefix(line, "Merge") || strings.HasPrefix(line, "Overlay plan:") {
				if !strings.Contains(body, line) {
					t.Errorf("scope %v differs from board state %q: %s", scope, line, body)
				}
			}
		}
	}
}
