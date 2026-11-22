package inject_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func sourceChangesTestReader(t *testing.T, withSourceHistory, mutating bool) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)
	surfaceID := "observe_investigate"
	if mutating {
		surfaceID = "implement_investigate"
	}
	plan := plans[surfaceID]
	if withSourceHistory {
		plan = plan.Promote("source_history")
	} else {
		plan = plan.Without("source_history")
	}
	r, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: surfaceID,
		Profiles:  profiles,
		Plan:      plan,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "sourceChangesTestReader", err)
	return r
}

func TestRenderSourceChangesBlockParity(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	reader := sourceChangesTestReader(t, true, true)
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
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, reader, "sess-brief-test", brief)
	for _, want := range []string{"main", "feature/x", "abc123def456", "fedcba654321", "14:01 UTC", "src/app.ts", "14:02 UTC", "@docs/guide.md", "refactor worker", "14:05 UTC"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered brief lost the supplied value %q:\n%s", want, got)
		}
	}
}

// The count may hold working-set files only when the last row is flagged, so
// the two flag shapes must not render the same claim about the counted files.
func TestACBRF03CountedWorkingSetFilesChangeTheElsewhereClaim(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	reader := sourceChangesTestReader(t, true, true)
	render := func(lastRowWorkingSet bool) string {
		return inject.RenderSourceChangesBlock(context.Background(), renderer, reader, "sess-brief-test", inject.SourceChangeBrief{
			Files: []inject.SourceChangeFile{
				{Path: "a.go", Actor: "user", Op: "edit", At: "14:02 UTC", Effects: 1, WorkingSet: true},
				{Path: "b.go", Actor: "user", Op: "edit", At: "14:03 UTC", Effects: 1, WorkingSet: lastRowWorkingSet},
			},
			OtherFiles:   5,
			OtherEffects: 7,
		})
	}
	outside, overflow := render(false), render(true)
	if outside == overflow {
		t.Fatalf("both flag shapes render the same claim:\n%s", outside)
	}
}

func TestACOC01OwnCommandRowsRenderDifferently(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	reader := sourceChangesTestReader(t, true, true)
	render := func(own bool) string {
		return inject.RenderSourceChangesBlock(context.Background(), renderer, reader, "sess-brief-test", inject.SourceChangeBrief{
			Files: []inject.SourceChangeFile{{Path: "gen.go", Actor: "external", Op: "create", At: "14:02 UTC", Effects: 1, DuringYourCommand: own}},
		})
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
	got := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, true), "sess-brief-test", brief)
	if !strings.Contains(got, "a.go") {
		t.Fatalf("rendered stored brief lost its row:\n%s", got)
	}
}

// AC-Q-01: what the brief prints is what the citation grammar reads back, row for
// row, and a brief stored before rows had handles prints none.
func TestRenderedBriefRowsParseAsTheirOwnCitations(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	reader := sourceChangesTestReader(t, true, true)
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
	cited := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, reader, "sess-brief-test", brief))
	if len(cited) != 2 || cited[0] != "source_change#4" || cited[1] != "source_change#5" {
		t.Fatalf("parsed handles = %v", cited)
	}

	brief.Git[0].Handle, brief.Files[0].Handle = "", ""
	if stored := handles(inject.RenderSourceChangesBlock(context.Background(), renderer, reader, "sess-brief-test", brief)); len(stored) != 0 {
		t.Fatalf("a brief with no handles parsed citations: %v", stored)
	}
}

func TestRenderSourceChangesBlockEmptyBriefRendersNothing(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	if got := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, true), "sess-brief-test", inject.SourceChangeBrief{}); got != "" {
		t.Fatalf("empty brief rendered %q, want nothing", got)
	}
}

// Paired witnesses: source_history offered vs loadable via request_tools vs unoffered,
// and mutating vs read-only editing warning.
func TestTruncatedLineAndMutationWarningFollowReaderState(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)

	offered := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, true), "sess-brief-test", inject.SourceChangeBrief{Truncated: true})
	withheld := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, false, false), "sess-brief-test", inject.SourceChangeBrief{Truncated: true})
	if !strings.Contains(offered, "source_history()") || strings.Contains(withheld, "source_history") {
		t.Fatalf("source_history offered/withheld mismatch:\noffered: %s\nwithheld: %s", offered, withheld)
	}

	// Paired witness: deferred/loadable source_history on explore_readonly teaches request_tools(need="source_history").
	exploreFloor, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "explore_readonly",
		Profiles:  profiles,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "exploreFloor reader", err)
	loadable := inject.RenderSourceChangesBlock(context.Background(), renderer, exploreFloor, "sess-brief-test", inject.SourceChangeBrief{Truncated: true})
	_ = plans
	if strings.Contains(loadable, "source_history()") || !strings.Contains(loadable, `request_tools(need="source_history")`) {
		t.Fatalf("loadable witness must teach request_tools(need=\"source_history\"):\n%s", loadable)
	}

	briefWithFile := inject.SourceChangeBrief{Files: []inject.SourceChangeFile{{Path: "a.go", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}}}
	mutBlock := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, true), "sess-brief-test", briefWithFile)
	roBlock := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, false), "sess-brief-test", briefWithFile)
	if !strings.Contains(mutBlock, "Before editing, read the changed file") {
		t.Fatalf("mutating witness missing before-editing warning:\n%s", mutBlock)
	}
	if strings.Contains(roBlock, "Before editing") {
		t.Fatalf("read-only witness leaked before-editing warning:\n%s", roBlock)
	}
}

func TestCompleteBriefRendersTheSameRegardlessOfSourceHistory(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	brief := inject.SourceChangeBrief{Files: []inject.SourceChangeFile{{Path: "a.go", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}}}
	offered := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, true, true), "sess-brief-test", brief)
	if withheld := inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, false, true), "sess-brief-test", brief); offered == "" || offered != withheld {
		t.Fatalf("a brief with no truncated line changed with source_history:\n%s\n%s", offered, withheld)
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
		return inject.RenderSourceChangesBlock(context.Background(), renderer, sourceChangesTestReader(t, offered, true), "sess-brief-test", brief)
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
