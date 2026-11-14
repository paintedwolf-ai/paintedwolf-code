package packboard_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIncompleteOrientationCannotAssertRepositoryEmptiness(t *testing.T) {
	brief := api.RepoBrief{GeneratedAt: time.Now(), Incomplete: true}
	if packboard.RepoKnownEmpty(brief) {
		t.Fatal("incomplete discovery became authoritative emptiness")
	}
	brief.FileCount = 3
	if line := packboard.FormatRepoLine(brief); !strings.Contains(line, "observed files") || !strings.Contains(line, "incomplete coverage") || strings.Contains(line, "refreshing") {
		t.Fatalf("settled incomplete orientation: %q", line)
	}
}

func TestFormatInjectBodyScopedRespectsBudget(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	langs := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		langs = append(langs, strings.Repeat("x", 20))
	}
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{Languages: langs, FileCount: 9999},
	}
	lines, _ := packboard.FormatInjectBodyScoped(snap, packboard.InjectScopeFull, false, now, api.MaxBoardInjectChars)
	body := strings.Join(lines, "\n")
	if len(body) > api.MaxBoardInjectChars {
		t.Fatalf("len=%d want <=%d body=%q", len(body), api.MaxBoardInjectChars, body)
	}
}

func TestFormatGitLineNone(t *testing.T) {
	got := packboard.FormatGitLine(&api.BoardGitSlice{Available: false}, api.RepoBrief{})
	if got != packboard.GitNoneOrientationLine {
		t.Fatalf("got %q", got)
	}
}

func TestFormatGitLineNoneEmptyRepoDropsSurveySteer(t *testing.T) {
	emptyRepo := api.RepoBrief{GeneratedAt: time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)}
	got := packboard.FormatGitLine(&api.BoardGitSlice{Available: false}, emptyRepo)
	if got != packboard.GitNoneEmptyRepoOrientationLine {
		t.Fatalf("got %q, want %q", got, packboard.GitNoneEmptyRepoOrientationLine)
	}
}

func TestFormatGitLineRepository(t *testing.T) {
	got := packboard.FormatGitLine(&api.BoardGitSlice{Available: true, Branch: "main", Dirty: true, StagedCount: 1, UnstagedCount: 1}, api.RepoBrief{})
	if got != "Git: main · dirty (2)" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatWorktreeLineBound(t *testing.T) {
	got := packboard.FormatWorktreeLine(&api.BoardGitSlice{
		Worktree: &api.BoardGitWorktree{
			Branch: "session/abc", BaseBranch: "main", AheadOfBase: 2, BehindBase: 1, Dirty: true,
		},
	})
	want := "Worktree: session/abc from main · 2 ahead · 1 behind · uncommitted"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatWorktreeLineOmitsWhenUnbound(t *testing.T) {
	if got := packboard.FormatWorktreeLine(&api.BoardGitSlice{}); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := packboard.FormatWorktreeLine(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatGitLineUnchangedWithWorktree(t *testing.T) {
	git := &api.BoardGitSlice{Available: true, Branch: "main", Dirty: false}
	before := packboard.FormatGitLine(git, api.RepoBrief{})
	git.Worktree = &api.BoardGitWorktree{Branch: "session/x", BaseBranch: "main", AheadOfBase: 1}
	after := packboard.FormatGitLine(git, api.RepoBrief{})
	if before != after {
		t.Fatalf("FormatGitLine changed: %q -> %q", before, after)
	}
}

func TestFormatOtherReposLineOmitsClean(t *testing.T) {
	got := packboard.FormatOtherReposLine(&api.BoardGitSlice{
		Others: []api.BoardGitRepoLine{
			{RepoID: "r1", Label: "clean", Branch: "main", Dirty: false},
			{RepoID: "r2", Label: "beta", Branch: "feat", Dirty: true, StagedCount: 1, UnstagedCount: 1},
		},
		OthersTruncated: 1,
	})
	want := "Other repos: beta feat · dirty (2) · +1 more"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatOtherReposLineEmptyWhenAllClean(t *testing.T) {
	got := packboard.FormatOtherReposLine(&api.BoardGitSlice{
		Others: []api.BoardGitRepoLine{{RepoID: "r1", Label: "beta", Branch: "main", Dirty: false}},
	})
	if got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatRepoLine(t *testing.T) {
	got := packboard.FormatRepoLine(api.RepoBrief{Languages: []string{"Go", "TypeScript"}, FileCount: 42})
	if got != "Repo: 42 files · Go+TypeScript" {
		t.Fatalf("got %q", got)
	}
	// Unmeasured briefs omit the repository line.
	if line := packboard.FormatRepoLine(api.RepoBrief{FileCount: 0}); line != "" {
		t.Fatalf("uncomputed repo line = %q, want empty string", line)
	}
	// Measured empty workspace states emptiness explicitly.
	measuredEmpty := api.RepoBrief{GeneratedAt: time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)}
	if line := packboard.FormatRepoLine(measuredEmpty); line != packboard.RepoEmptyOrientationLine {
		t.Fatalf("measured empty repo line = %q, want %q", line, packboard.RepoEmptyOrientationLine)
	}
}

func TestBuildOrientationLinesIncludesRepoLine(t *testing.T) {
	lines := packboard.BuildOrientationLines(api.BoardSnapshot{
		Host: &api.BoardHostSlice{
			OS:              "darwin",
			Arch:            "arm64",
			ExecutionTarget: api.ExecutionTargetLocal,
			Shell:           true,
		},
		Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 7},
	}, packboard.OrientOpts{Now: time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)})
	for _, line := range lines {
		if line == "Host: darwin/arm64 · local sidecar · shell" {
			goto foundHost
		}
	}
	t.Fatalf("lines = %v missing Host line", lines)
foundHost:
	for _, line := range lines {
		if line == "Repo: 7 files · Go" {
			return
		}
	}
	t.Fatalf("lines = %v", lines)
}

func TestFormatScansLineFailedShowsError(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	line := packboard.FormatScansLine(&api.BoardScanSummary{
		Status:        api.CodeScanStatusFailed,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 0,
		Error:         "no scanner for categories [all]",
		CreatedAt:     now.Add(-time.Minute),
		CompletedAt:   &now,
	}, nil, "", now)
	if !strings.Contains(line, "failed") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, "err:") {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatScansLineCompleteShowsRuleWarning(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-4 * time.Minute)
	line := packboard.FormatScansLine(&api.BoardScanSummary{
		Status:         api.CodeScanStatusComplete,
		Categories:     []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount:  0,
		WarningSummary: []api.ScanWarningSummary{{Kind: api.ScanWarningRuleParseError, Count: 1, Rules: 1}},
		CreatedAt:      completed,
		CompletedAt:    &completed,
	}, nil, "", now)
	if !strings.Contains(line, "complete") || strings.Contains(line, "clean") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, "warn:") || !strings.Contains(line, "1 analysis limitation") {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatScansLineIncludesHeadAndStaleHint(t *testing.T) {
	now := time.Date(2026, 7, 12, 18, 0, 0, 0, time.UTC)
	completed := now.Add(-4 * time.Minute)
	line := packboard.FormatScansLine(&api.BoardScanSummary{
		AssessmentID:  "53560832-f225-4f80-9251-d63e500b9d0b",
		Status:        api.CodeScanStatusComplete,
		HeadShort:     "fbc47ebc",
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 622,
		FindingsByLevel: map[string]int{
			string(api.FindingLevelCritical): 2,
			string(api.FindingLevelMedium):   5,
		},
		TopLocations: []api.BoardScanTopLocation{{URI: "electron/index.cjs", StartLine: 288}},
		CreatedAt:    completed,
		CompletedAt:  &completed,
	}, nil, "deadbeef", now)
	for _, want := range []string{"assessment 53560832", "head fbc47ebc", "622 findings", "2err 5warn", "top: electron/index.cjs:288", "stale vs git"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q missing %q", line, want)
		}
	}
}

func TestFormatScansLineZeroFindingsCleanWithHead(t *testing.T) {
	now := time.Date(2026, 7, 12, 18, 0, 0, 0, time.UTC)
	line := packboard.FormatScansLine(&api.BoardScanSummary{
		Status:        api.CodeScanStatusComplete,
		HeadShort:     "abc12345",
		FindingsCount: 0,
		CreatedAt:     now,
		CompletedAt:   &now,
	}, nil, "abc12345", now)
	if !strings.Contains(line, "clean") || !strings.Contains(line, "head abc12345") {
		t.Fatalf("line = %q", line)
	}
	if strings.Contains(line, "stale vs git") {
		t.Fatalf("matching head should not be stale: %q", line)
	}
}

func TestFormatScansLineRegressionTails(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Minute)
	summary := &api.BoardScanSummary{
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 5,
		CreatedAt:     completed,
		CompletedAt:   &completed,
	}
	cases := []struct {
		name    string
		compare *api.BoardScanCompareSlice
		want    string
	}{
		{
			name: "+2 new medium",
			compare: &api.BoardScanCompareSlice{
				BaselineAssessmentID: "a1b2c3d4-1111-2222-3333-444455556666",
				NewCount:             2,
				NewByLevel:           map[string]int{string(api.FindingLevelMedium): 2},
			},
			want: "+2 new",
		},
		{
			name: "regression critical",
			compare: &api.BoardScanCompareSlice{
				BaselineAssessmentID: "a1b2c3d4-1111-2222-3333-444455556666",
				NewCount:             1,
				NewByLevel:           map[string]int{string(api.FindingLevelCritical): 1},
			},
			want: "regression · +1 critical",
		},
		{
			name: "resolved only",
			compare: &api.BoardScanCompareSlice{
				BaselineAssessmentID: "a1b2c3d4-1111-2222-3333-444455556666",
				ResolvedCount:        2,
			},
			want: "−2 resolved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := packboard.FormatScansLine(summary, tc.compare, "", now)
			if !strings.Contains(line, tc.want) {
				t.Fatalf("line = %q want contains %q", line, tc.want)
			}
			if !strings.Contains(line, "prior assessment a1b2c3d4") {
				t.Fatalf("line = %q want prior assessment short id", line)
			}
		})
	}
}

func TestFormatScansDetailLinesIncludesTopNewStubs(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Minute)
	lines := packboard.FormatScansDetailLines(&api.BoardScansSlice{
		CurrentAssessment: &api.BoardScanSummary{
			Status:        api.CodeScanStatusComplete,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			FindingsCount: 2,
			FindingsByKind: map[string]int{
				string(api.FindingKindSecret): 2,
			},
			CreatedAt:   completed,
			CompletedAt: &completed,
		},
		Compare: &api.BoardScanCompareSlice{
			NewCount: 1,
			TopNew: []api.BoardScanCompareStub{
				{File: "secrets.env", RuleID: "gitleaks:token"},
			},
		},
	}, "", now)
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "secrets.env:gitleaks:token") {
		t.Fatalf("detail lines = %q", body)
	}
}

func TestBuildOrientationLinesOmitsEmptyRepo(t *testing.T) {
	lines := packboard.BuildOrientationLines(api.BoardSnapshot{
		Repo: api.RepoBrief{},
	}, packboard.OrientOpts{Now: time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)})
	for _, line := range lines {
		if strings.HasPrefix(line, "Repo:") {
			t.Fatalf("unexpected repo line: %q", line)
		}
	}
}


// The coordinator addresses workers by name and results by handle, so no
// board line, in any scope, carries a job id or a prefix of one.
func TestBoardOrientationNamesNoJob(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	job := func(n int) string { return fmt.Sprintf("%08x-1111-4111-8111-111111111111", 0xabc00000+n) }
	var tasks []api.WorkerTask
	for i, status := range []api.WorkerMergeStatus{api.WorkerMergeStatusPending, api.WorkerMergeStatusMerged, api.WorkerMergeStatusRejected, api.WorkerMergeStatusOrphaned, api.WorkerMergeStatusRebasing} {
		tasks = append(tasks, api.WorkerTask{ID: job(i), AgentType: "implementer", Status: api.WorkerStatusComplete, MergeStatus: status, WorkspaceRoot: "/tmp/w", ChildSessionID: "child",
			CreatedAt: now, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "partial"}}})
	}
	tasks = append(tasks, api.WorkerTask{ID: job(9), AgentType: "repo-researcher", Status: api.WorkerStatusRunning, Brief: "read it", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}})
	workers := api.BoardWorkersSlice{
		"tasks":         tasks,
		"promote_paths": []api.WorkerPromoteJobPathStatus{{WorkerID: job(0), Paths: []api.WorkerPromotePathStatus{{Path: "a.go", Status: api.WorkerPromotePathOutcomeConflict, HunkCount: 1}}}},
		"overlay_merge_plan": &api.OverlayMergePlan{PendingCount: 2, PromoteSequence: []string{"r-docs", "r-fixer"},
			SharedPaths: []api.OverlayMergeSharedPath{{Path: "a.go", OverlayIDs: []string{"r-docs", "r-fixer"}}},
			Entries:     []api.OverlayMergePlanEntry{{OverlayID: "r-docs", Worker: "docs"}, {OverlayID: "r-fixer", Worker: "fixer"}}},
	}
	snap := api.BoardSnapshot{Workers: &workers}
	lines := packboard.BuildOrientationLines(snap, packboard.OrientOpts{Now: now})
	for _, scope := range []packboard.InjectScope{packboard.InjectScopeFull, packboard.InjectScopePulse} {
		body, _ := packboard.FormatInjectBodyScoped(snap, scope, false, now, api.MaxBoardInjectChars)
		lines = append(lines, body...)
	}
	lines = append(lines, packboard.FormatReservedPathsLines([]api.BoardReservationEntry{{Path: "a.go", JobID: job(0), LegLabel: "fix"}})...)
	if len(lines) == 0 {
		t.Fatal("no board lines")
	}
	for _, line := range lines {
		for _, task := range tasks {
			if strings.Contains(line, task.ID[:4]) {
				t.Fatalf("line names job %s: %q", task.ID, line)
			}
		}
	}
}
