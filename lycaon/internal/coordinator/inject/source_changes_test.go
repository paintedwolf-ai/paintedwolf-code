package inject_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
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

// AC-Q-01: what the brief prints is what the citation grammar reads back, row for
// row, and a brief stored before rows had handles prints none.
func TestRenderedBriefRowsParseAsTheirOwnCitations(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	brief := inject.SourceChangeBrief{
		Git:   []inject.SourceGitLine{{Handle: "source_change#4", Kind: "commit", ToRef: "main", At: "14:01 UTC"}},
		Files: []inject.SourceChangeFile{{Handle: "source_change#5", Path: "config.txt", Actor: "external", Op: "write", At: "14:02 UTC", Effects: 1}},
	}
	handles := func(block string) []string {
		var out []string
		for _, occurrence := range citationmd.Parse(block, citationmd.Options{}).Occurrences {
			if occurrence.Kind == citationmd.KindCitation {
				out = append(out, occurrence.Citation.Handle)
			}
		}
		return out
	}
	cited := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief))
	if len(cited) != 2 || cited[0] != "source_change#4" || cited[1] != "source_change#5" {
		t.Fatalf("parsed handles = %v", cited)
	}

	brief.Git[0].Handle, brief.Files[0].Handle = "", ""
	if stored := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief)); len(stored) != 0 {
		t.Fatalf("a brief with no handles parsed citations: %v", stored)
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
