package inject

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildWorkerLegInjectData(t *testing.T) {
	data := BuildWorkerLegInjectData(WorkerLegContext{
		LegID:              "leg-1",
		WorkflowID:         "wf",
		PhaseID:            "implement",
		CompletionCriteria: []string{"tests pass"},
		LegTools:           []string{"read"},
		Checklist:          []string{"step one"},
	})
	if data.LegID != "leg-1" || len(data.Checklist) != 1 || data.Checklist[0].Index != 1 {
		t.Fatalf("data = %+v", data)
	}
}

func TestRenderWorkerLegInject(t *testing.T) {
	renderer := testInjectRenderer(t)
	block, err := RenderWorkerLegInject(context.Background(), renderer, "sess-inject-test", WorkerLegContext{
		LegID:     "leg-1",
		PhaseID:   "implement",
		LegTools:  []string{"read"},
		Checklist: []string{"do work"},
	})
	testutil.FailErr(t, "RenderWorkerLegInject failed", err)
	for _, want := range []string{WorkerLegInjectSentinel, "## Leg", "leg-1", "phase_id: implement", "## Leg tools", "- read"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in block = %q", want, block)
		}
	}
	if strings.Contains(block, "## Sibling notes") {
		t.Fatalf("sibling notes block rendered with no notes: %q", block)
	}
}

func TestRenderWorkerLegInjectSiblingNotes(t *testing.T) {
	renderer := testInjectRenderer(t)
	block, err := RenderWorkerLegInject(context.Background(), renderer, "sess-inject-test", WorkerLegContext{
		LegID:    "leg-1",
		LegTools: []string{"read"},
		SiblingNotes: []SiblingNote{
			{Agent: "repo-researcher", Summary: "config in resolve.go:40", Ref: "resolve.go:40"},
		},
	})
	testutil.FailErr(t, "RenderWorkerLegInject failed", err)
	for _, want := range []string{"## Peer findings", "**repo-researcher:**", "config in resolve.go:40", "(resolve.go:40)"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in block = %q", want, block)
		}
	}
}

func TestRenderBoardOrientationInject_NilRenderer(t *testing.T) {
	_, err := RenderBoardOrientationInject(context.Background(), nil, "sess-inject-test", api.BoardSnapshot{}, packboard.InjectScopeFull, false, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildBoardInjectData(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 12},
		Git:  &api.BoardGitSlice{Available: true, Branch: "main", Dirty: true, StagedCount: 1, UnstagedCount: 2},
	}
	data := BuildBoardInjectData(snap, packboard.InjectScopeFull, false, true, now)
	if data.NowLine == "" || !strings.HasPrefix(data.NowLine, "Now: ") {
		t.Fatalf("now_line = %q", data.NowLine)
	}
	if len(data.Lines) == 0 {
		t.Fatal("expected orientation lines")
	}
}

func TestBuildBoardInjectDataPulseOmitsStableLines(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 12},
		Git:  &api.BoardGitSlice{Available: false},
	}
	data := BuildBoardInjectData(snap, packboard.InjectScopePulse, false, true, now)
	for _, line := range data.Lines {
		if strings.HasPrefix(line.Text, "Repo:") || strings.HasPrefix(line.Text, "Git:") {
			t.Fatalf("pulse inject leaked stable line %q", line.Text)
		}
	}
}

func TestRenderBoardOrientationInject_CharBudget(t *testing.T) {
	renderer := testInjectRenderer(t)
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	langs := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		langs = append(langs, strings.Repeat("x", 20))
	}
	snap := api.BoardSnapshot{
		Repo:        api.RepoBrief{Languages: langs, FileCount: 9999},
		DetailLevel: api.BoardDetailLevelCompact,
	}
	block, err := RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", snap, packboard.InjectScopeFull, false, true, now)
	testutil.FailErr(t, "RenderBoardOrientationInject failed", err)
	packStart := strings.Index(block, packboard.PackBoardSentinel)
	if packStart < 0 {
		t.Fatal("missing pack-board sentinel")
	}
	packBody := strings.TrimSpace(block[packStart+len(packboard.PackBoardSentinel):])
	if len(packBody) > api.MaxBoardInjectChars+4 {
		t.Fatalf("pack body len %d exceeds %d: %q", len(packBody), api.MaxBoardInjectChars, packBody)
	}
}

func TestRenderBoardOrientationInject_RepoLine(t *testing.T) {
	renderer := testInjectRenderer(t)
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{
		Repo:        api.RepoBrief{Languages: []string{"Go", "TypeScript"}, FileCount: 142},
		DetailLevel: api.BoardDetailLevelCompact,
	}
	block, err := RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", snap, packboard.InjectScopeFull, false, true, now)
	testutil.FailErr(t, "RenderBoardOrientationInject failed", err)
	if !strings.Contains(block, "Repo: 142 files · Go+TypeScript") {
		t.Fatalf("block missing repo line: %q", block)
	}
}

func TestRenderBoardOrientationInject_OmitsEmptyRepo(t *testing.T) {
	renderer := testInjectRenderer(t)
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{DetailLevel: api.BoardDetailLevelCompact}
	block, err := RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", snap, packboard.InjectScopeFull, false, true, now)
	testutil.FailErr(t, "RenderBoardOrientationInject failed", err)
	// The legend can include every board label.
	if strings.Contains(boardLinesOnly(t, block), "Repo:") {
		t.Fatalf("empty repo brief should not render Repo line: %q", block)
	}
}

// boardLinesOnly returns the rendered board, dropping the legend that precedes it.
func boardLinesOnly(t *testing.T, block string) string {
	t.Helper()
	const marker = "<!-- pack-board:v1 -->"
	idx := strings.Index(block, marker)
	if idx < 0 {
		t.Fatalf("board inject carries no %s marker: %q", marker, block)
	}
	return block[idx+len(marker):]
}

func TestRenderWorkerLegInject_MissingSentinel(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("inject/worker-leg.md", "## broken"); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	renderer := prompts.NewInjectRenderer(engine)
	_, err := RenderWorkerLegInject(context.Background(), renderer, "sess-inject-test", WorkerLegContext{LegID: "leg-1"})
	if err == nil || !strings.Contains(err.Error(), WorkerLegInjectSentinel) {
		t.Fatalf("err = %v", err)
	}
}
