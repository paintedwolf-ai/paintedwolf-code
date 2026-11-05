package inject_test

import (
	"context"
	"encoding/json"
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
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, true)
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

// The count may hold working-set files only when the last row is flagged, so
// the two flag shapes must not render the same claim about the counted files.
func TestACBRF03CountedWorkingSetFilesChangeTheElsewhereClaim(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	render := func(lastRowWorkingSet bool) string {
		return inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{
			Files: []inject.SourceChangeFile{
				{Path: "a.go", Actor: "user", Op: "edit", At: "14:02 UTC", Effects: 1, WorkingSet: true},
				{Path: "b.go", Actor: "user", Op: "edit", At: "14:03 UTC", Effects: 1, WorkingSet: lastRowWorkingSet},
			},
			OtherFiles:   5,
			OtherEffects: 7,
		}, true)
	}
	outside, overflow := render(false), render(true)
	if outside == overflow {
		t.Fatalf("both flag shapes render the same claim:\n%s", outside)
	}
}

func TestACOC01OwnCommandRowsRenderDifferently(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	render := func(own bool) string {
		return inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{
			Files: []inject.SourceChangeFile{{Path: "gen.go", Actor: "external", Op: "create", At: "14:02 UTC", Effects: 1, DuringYourCommand: own}},
		}, true)
	}
	if flagged, plain := render(true), render(false); flagged == plain || plain == "" {
		t.Fatalf("own command row renders like a foreign row:\n%s", flagged)
	}
}

func TestACOC01StoredBriefsKeepTheirElsewhereClaim(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
		want   bool
	}{
		{"before any row flag", `{"files":[{"path":"a.go","actor":"user"}],"other_files":2,"other_effects":3}`, false},
		{"flagged last row", `{"files":[{"path":"a.go","actor":"user","working_set":true}],"other_files":2,"other_effects":3}`, true},
		{"unflagged last row", `{"files":[{"path":"a.go","actor":"user","working_set":true},{"path":"b.go","actor":"user"}],"other_files":2,"other_effects":3}`, false},
		{"nothing counted", `{"files":[{"path":"a.go","actor":"user","working_set":true}]}`, false},
		{"recorded counted working set", `{"files":[{"path":"a.go","actor":"user"}],"other_files":2,"other_effects":3,"other_working_set":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var brief inject.SourceChangeBrief
			if err := json.Unmarshal([]byte(tc.stored), &brief); err != nil {
				t.Fatalf("unmarshal stored brief: %v", err)
			}
			if got := brief.OtherMayIncludeWorkingSet(); got != tc.want {
				t.Fatalf("OtherMayIncludeWorkingSet = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestACBRF06StoredRowsRenderLikeUnflaggedRows(t *testing.T) {
	const stored = `{"files":[{"path":"a.go","actor":"user","op":"write","at":"14:02 UTC","effects":1}],"other_files":2,"other_effects":3}`
	var brief inject.SourceChangeBrief
	if err := json.Unmarshal([]byte(stored), &brief); err != nil {
		t.Fatalf("unmarshal stored brief: %v", err)
	}
	if len(brief.Files) != 1 || brief.Files[0].WorkingSet {
		t.Fatalf("stored rows = %+v, want one unflagged row", brief.Files)
	}
	renderer := promptstest.InjectRenderer(t)
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, true)
	for _, want := range []string{"- a.go — user write at 14:02 UTC", "Elsewhere in the project: 3 change(s) across 2 file(s)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered stored brief missing %q:\n%s", want, got)
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
	cited := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, true))
	if len(cited) != 2 || cited[0] != "source_change#4" || cited[1] != "source_change#5" {
		t.Fatalf("parsed handles = %v", cited)
	}

	brief.Git[0].Handle, brief.Files[0].Handle = "", ""
	if stored := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, true)); len(stored) != 0 {
		t.Fatalf("a brief with no handles parsed citations: %v", stored)
	}
}

func TestRenderSourceChangesBlockEmptyBriefRendersNothing(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	if got := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{}, true); got != "" {
		t.Fatalf("empty brief rendered %q, want nothing", got)
	}
}

// A surface without source_history is not told to call it.
func TestTruncatedLineNamesSourceHistoryOnlyWhenOffered(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	render := func(offered bool) string {
		return inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", inject.SourceChangeBrief{Truncated: true}, offered)
	}
	offered, withheld := render(true), render(false)
	if withheld == "" || len(offered) <= len(withheld) {
		t.Fatalf("offered must render the longer line:\noffered: %s\nwithheld: %s", offered, withheld)
	}
}

func TestCompleteBriefRendersTheSameOnEverySurface(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	brief := inject.SourceChangeBrief{Files: []inject.SourceChangeFile{{Path: "a.go", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}}}
	offered := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, true)
	if withheld := inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, false); offered == "" || offered != withheld {
		t.Fatalf("a brief with no truncated line changed with the surface:\n%s\n%s", offered, withheld)
	}
}

// A stored brief says only truncated; the own class says no other actor's change
// is missing, which is a different line from the foreign class's.
func TestTruncatedLineStatesEachClass(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	render := func(stored string, offered bool) string {
		var brief inject.SourceChangeBrief
		if err := json.Unmarshal([]byte(stored), &brief); err != nil {
			t.Fatalf("unmarshal stored brief: %v", err)
		}
		return inject.RenderSourceChangesBlock(context.Background(), renderer, "sess-brief-test", brief, offered)
	}
	const row = `"files":[{"path":"a.go","actor":"user","op":"write","at":"14:02 UTC","effects":1}]`
	complete := render(`{`+row+`}`, true)
	foreign := render(`{`+row+`,"truncated":true}`, true)
	ownOnly := render(`{`+row+`,"own_truncated":true}`, true)
	both := render(`{`+row+`,"truncated":true,"own_truncated":true}`, true)
	if foreign == complete || ownOnly == complete || ownOnly == foreign {
		t.Fatalf("classes rendered alike:\ncomplete: %s\nforeign: %s\nown: %s", complete, foreign, ownOnly)
	}
	if both != foreign {
		t.Fatalf("a foreign overflow must decide the line:\n%s\n%s", both, foreign)
	}
	if stored := render(`{"truncated":true}`, true); stored == "" {
		t.Fatal("a stored brief with only truncated stopped rendering")
	}
	if withheld := render(`{`+row+`,"own_truncated":true}`, false); len(withheld) >= len(ownOnly) || withheld == complete {
		t.Fatalf("own line without the tool:\n%s", withheld)
	}
}
