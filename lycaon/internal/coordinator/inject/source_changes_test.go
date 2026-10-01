package inject_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

func TestRenderSourceChangesBlockParity(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	brief := inject.SourceChangeBrief{
		Files: []inject.SourceChangeFile{
			{Path: "src/app.ts", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1},
			{Path: "@docs/guide.md", Actor: "agent", Detail: "refactor worker", Op: "write", At: "14:05 UTC", Effects: 3},
		},
		Git: []inject.SourceGitLine{
			{Kind: "checkout", FromRef: "main", ToRef: "feature/x", FromCommit: "abc123def456", ToCommit: "fedcba654321", At: "14:01 UTC"},
		},
		OtherFiles:   2,
		OtherEffects: 5,
	}
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief)
	for _, want := range []string{
		"## Source changes since your last turn",
		"- git checkout main → feature/x (abc123def456 → fedcba654321) at 14:01 UTC",
		"- src/app.ts — user write at 14:02 UTC",
		"- @docs/guide.md — agent (refactor worker) write (3 changes, newest at 14:05 UTC)",
		"Elsewhere in the project: 5 change(s) across 2 file(s)",
		"recorded cause of the file changes",
		"use a newer read from this turn",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered brief missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "recording cap") {
		t.Fatalf("untruncated brief mentions the cap:\n%s", got)
	}
}

func TestRenderSourceChangesBlockEmptyBriefRendersNothing(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	if got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{}); got != "" {
		t.Fatalf("empty brief rendered %q, want nothing", got)
	}
}

func TestRenderSourceChangesBlockTruncationIsStated(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{
		Truncated: true,
	})
	if !strings.Contains(got, "counts are a floor") || !strings.Contains(got, "source_history") {
		t.Fatalf("truncated brief must state the floor and the full-record tool:\n%s", got)
	}
}
