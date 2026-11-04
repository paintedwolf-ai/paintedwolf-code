package sourcebrief

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func briefEffect(fileID, path string, ordinal int64, origin api.SourceChangeOrigin, sessionID string) sourceledger.Effect {
	return sourceledger.Effect{
		FileID: fileID, RootID: "r1", Path: path,
		Origin: origin, SessionID: sessionID, Op: api.SourceChangeOpWrite,
		Ordinal: ordinal, TS: time.Date(2026, 8, 26, 14, 2, 0, 0, time.UTC),
	}
}

func briefRoots() []projectroot.RootRef {
	return []projectroot.RootRef{
		{ID: "r1", Label: "root", IsPrimary: true},
		{ID: "r2", Label: "docs"},
	}
}

// briefFiles builds one user effect per file, newest first by slice order.
func briefFiles(prefix string, count int, startOrdinal int64) []sourceledger.Effect {
	effects := make([]sourceledger.Effect, 0, count)
	for i := range count {
		path := fmt.Sprintf("%s%02d.go", prefix, i)
		effects = append(effects, briefEffect("file-"+path, path, startOrdinal-int64(i), api.SourceChangeOriginUser, ""))
	}
	return effects
}

func briefTouched(effects []sourceledger.Effect) map[string]bool {
	touched := make(map[string]bool, len(effects))
	for _, effect := range effects {
		touched[effect.Path] = true
	}
	return touched
}

func briefRowPaths(brief inject.SourceChangeBrief, workingSet bool) []string {
	var paths []string
	for _, row := range brief.Files {
		if row.WorkingSet == workingSet {
			paths = append(paths, row.Path)
		}
	}
	return paths
}

func effectPaths(effects []sourceledger.Effect) []string {
	paths := make([]string, 0, len(effects))
	for _, effect := range effects {
		paths = append(paths, effect.Path)
	}
	return paths
}

func TestAssembleSourceChangeBriefPartitionsWorkingSet(t *testing.T) {
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{"a.go": true},
		Effects: []sourceledger.Effect{
			// Newest first: an untouched file leads, then two effects on a touched file.
			briefEffect("f2", "b.go", 10, api.SourceChangeOriginExternal, ""),
			briefEffect("f1", "a.go", 9, api.SourceChangeOriginUser, ""),
			briefEffect("f1", "a.go", 7, api.SourceChangeOriginUser, ""),
		},
	})
	want := []inject.SourceChangeFile{
		{Path: "a.go", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 2, WorkingSet: true},
		{Path: "b.go", Actor: "external", Op: "write", At: "14:02 UTC", Effects: 1},
	}
	if !slices.Equal(brief.Files, want) {
		t.Fatalf("itemized rows = %+v, want %+v", brief.Files, want)
	}
	if brief.OtherFiles != 0 || brief.OtherEffects != 0 {
		t.Fatalf("elsewhere = %d files / %d effects, want none when everything fits", brief.OtherFiles, brief.OtherEffects)
	}
}

func TestACBRF01ForeignFilesOutsideTheWorkingSetAreItemized(t *testing.T) {
	effects := briefFiles("other", 3, 100)
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: map[string]bool{}, Effects: effects,
	})
	if got := briefRowPaths(brief, false); !slices.Equal(got, effectPaths(effects)) {
		t.Fatalf("rows = %v, want every file newest first", got)
	}
	if len(brief.Files) != 3 || brief.OtherFiles != 0 || brief.OtherEffects != 0 {
		t.Fatalf("brief = %+v, want three rows and nothing counted", brief)
	}
	if brief.OtherMayIncludeWorkingSet() {
		t.Fatal("nothing was counted, so nothing counted can be in the working set")
	}
}

func TestACBRF02WorkingSetLeadsAndOtherFilesShareTheCap(t *testing.T) {
	others := briefFiles("other", 10, 1000)
	workingSet := briefFiles("work", 15, 500)
	// Other files are newer, so only the partition can put the working set first.
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: briefTouched(workingSet),
		Effects: slices.Concat(others, workingSet),
	})
	if len(brief.Files) != sourceChangeBriefFileCap {
		t.Fatalf("rows = %d, want the cap", len(brief.Files))
	}
	for i, row := range brief.Files {
		if row.WorkingSet != (i < 15) {
			t.Fatalf("row %d = %+v, want the working set in the first 15 rows only", i, row)
		}
	}
	if got := briefRowPaths(brief, true); !slices.Equal(got, effectPaths(workingSet)) {
		t.Fatalf("working-set rows = %v, want newest first", got)
	}
	if got := briefRowPaths(brief, false); !slices.Equal(got, effectPaths(others[:5])) {
		t.Fatalf("other rows = %v, want the five newest", got)
	}
	if brief.OtherFiles != 5 || brief.OtherEffects != 5 {
		t.Fatalf("counted = %d files / %d effects, want 5/5", brief.OtherFiles, brief.OtherEffects)
	}
	if brief.OtherMayIncludeWorkingSet() {
		t.Fatal("every working-set file was itemized, so the counted files are outside it")
	}
}

func TestACBRF03WorkingSetOverflowIsCountedAndNeverDisplaced(t *testing.T) {
	for _, tc := range []struct {
		name        string
		others      int
		wantCounted int
	}{
		{name: "working set alone", others: 0, wantCounted: 5},
		{name: "newer other files present", others: 3, wantCounted: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingSet := briefFiles("work", 25, 500)
			brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
				SessionID: "s1", Roots: briefRoots(), Touched: briefTouched(workingSet),
				Effects: slices.Concat(briefFiles("other", tc.others, 1000), workingSet),
			})
			if got := briefRowPaths(brief, true); !slices.Equal(got, effectPaths(workingSet[:sourceChangeBriefFileCap])) {
				t.Fatalf("working-set rows = %v, want the newest %d", got, sourceChangeBriefFileCap)
			}
			if len(brief.Files) != sourceChangeBriefFileCap {
				t.Fatalf("rows = %d, want only working-set rows at the cap", len(brief.Files))
			}
			if brief.OtherFiles != tc.wantCounted || brief.OtherEffects != tc.wantCounted {
				t.Fatalf("counted = %d files / %d effects, want %d", brief.OtherFiles, brief.OtherEffects, tc.wantCounted)
			}
			if !brief.OtherMayIncludeWorkingSet() {
				t.Fatal("working-set files were counted, so the brief must say the count may include them")
			}
		})
	}
}

func TestACBRF05OwnEffectsNeverRowOrCount(t *testing.T) {
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{"own.go": true},
		Effects: []sourceledger.Effect{
			briefEffect("f1", "own.go", 9, api.SourceChangeOriginAgent, "s1"),
			briefEffect("f2", "own-elsewhere.go", 8, api.SourceChangeOriginAgent, "s1"),
		},
	})
	if !brief.Empty() {
		t.Fatalf("brief = %+v, want empty for the session's own effects", brief)
	}
}

func TestACBRF04WorkerBranchEffectsAreExcluded(t *testing.T) {
	worker := briefEffect("f1", "a.go", 9, api.SourceChangeOriginAgent, "other")
	worker.BranchID = "worker-1"
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{"a.go": true},
		Effects: []sourceledger.Effect{worker},
	})
	if !brief.Empty() {
		t.Fatalf("brief = %+v, want empty — a worker branch is not this tree", brief)
	}
}

type briefWindowStore struct {
	Store
	turn int
}

func (s briefWindowStore) ReadSourceTurn(context.Context, string, int) (messageview.SourceTurn, error) {
	return messageview.SourceTurn{Number: s.turn}, nil
}

func (s briefWindowStore) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

type briefRootList []projectroot.RootRef

func (r briefRootList) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return r, nil
}

// The project-wide window spans branches; the per-file branch predicate must not narrow it.
func TestBuildWindowKeepsProjectWideEffects(t *testing.T) {
	t.Run("AC-BR-04 EffectsBetween and Build are unchanged by per-file scoping", func(t *testing.T) {
		sqlDB := testdbfixture.Open(t, "store.db")
		testdbseed.InsertProject(t, sqlDB, "p1")
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots
 (id, project_id, path, label, is_primary, added_at, kind)
 VALUES ('r1','p1','/tmp/brief-root','root',1,?,'attached')`, db.FormatTime(time.Now().UTC()))
		testutil.FailErr(t, "seed root", err)
		ledger := sourceledger.New(sqlDB, t.TempDir())
		checkpoint := func(turn int) {
			_, err := ledger.Checkpoints.CreateStructuralCheckpoint(t.Context(), sourceledger.StructuralCheckpointInput{
				ProjectID: "p1", Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: turn,
			})
			testutil.FailErr(t, "create checkpoint", err)
		}
		record := func(branch sourcebranch.ID, origin api.SourceChangeOrigin, job, before, after string) {
			op := api.SourceChangeOpWrite
			if before == "" {
				op = api.SourceChangeOpCreate
			}
			testutil.FailErr(t, "record effect", ledger.Record(t.Context(), sourceledger.RecordInput{
				RecordLocation: sourceledger.RecordLocation{RootID: "r1", Path: "a.go"},
				ProjectID:      "p1", BranchID: branch, JobID: job, Op: op, Origin: origin,
				Before: []byte(before), After: []byte(after),
			}))
		}
		record(sourcebranch.Trunk, api.SourceChangeOriginUser, "", "", "base\n")
		checkpoint(1)
		record(sourcebranch.Trunk, api.SourceChangeOriginUser, "", "base\n", "user\n")
		record(sourcebranch.ID("job-1"), api.SourceChangeOriginAgent, "job-1", "base\n", "worker\n")
		checkpoint(2)

		all, err := ledger.History.EffectsBetween(t.Context(), "p1", 0, 0, 10)
		testutil.FailErr(t, "list effects", err)
		if len(all) != 3 || all[0].BranchID != "job-1" || all[1].BranchID != sourcebranch.Trunk {
			t.Fatalf("effects = %+v, want all three across both branches", all)
		}

		service := New(briefWindowStore{turn: 2}, briefRootList(briefRoots()))
		service.SetSources(tools.SourceHistory{Files: ledger.History, Git: ledger.Git, Authorship: ledger.Walk}, ledger.Checkpoints)
		brief := service.Build(t.Context(), &api.Session{ID: "s1", ProjectID: "p1"})
		if len(brief.Files) != 1 || brief.Files[0].Effects != 1 || brief.OtherFiles != 0 || brief.Truncated {
			t.Fatalf("brief = %+v, want the single trunk change itemized", brief)
		}
	})
}

func TestAssembleSourceChangeBriefQualifiesNonPrimaryRoots(t *testing.T) {
	effect := briefEffect("f1", "guide.md", 9, api.SourceChangeOriginUser, "")
	effect.RootID = "r2"
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{"@docs/guide.md": true},
		Effects: []sourceledger.Effect{effect},
	})
	if len(brief.Files) != 1 || brief.Files[0].Path != "@docs/guide.md" {
		t.Fatalf("brief files = %+v, want @docs/guide.md itemized via display-path match", brief.Files)
	}
}

func TestAssembleSourceChangeBriefLeadsWithGitTransitions(t *testing.T) {
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{},
		// Newest first, as the ledger pages them.
		Transitions: []sourceledger.GitTransition{
			{
				RootID: "r1", Kind: "commit", FromCommit: "bbbbbbbbbbbb",
				ToCommit: "cccccccccccc", ToRef: "feature-x", Detail: "Fix the bug",
				ObservedTS: time.Date(2026, 8, 26, 14, 3, 0, 0, time.UTC),
			},
			{
				RootID: "r2", Kind: "checkout", FromCommit: "aaaaaaaaaaaa",
				ToCommit: "bbbbbbbbbbbb", FromRef: "main", ToRef: "feature-x",
				ObservedTS: time.Date(2026, 8, 26, 14, 1, 0, 0, time.UTC),
			},
		},
	})
	if brief.Empty() {
		t.Fatal("a window holding only ref movements must not read as empty")
	}
	if len(brief.Git) != 2 {
		t.Fatalf("git lines = %+v", brief.Git)
	}
	// Oldest first, so file rows read against their cause.
	first, second := brief.Git[0], brief.Git[1]
	if first.Kind != "checkout" || first.Root != "@docs" || first.FromRef != "main" ||
		first.FromCommit != "aaaaaaaa" || first.At != "14:01 UTC" {
		t.Fatalf("first git line = %+v", first)
	}
	if second.Kind != "commit" || second.Root != "" || second.Detail != "Fix the bug" ||
		second.ToCommit != "cccccccc" {
		t.Fatalf("second git line = %+v", second)
	}
}

// ownCommandFiles builds effects inside the session's own command window. They
// classify as external and are newest, so only the class can place them last.
func ownCommandFiles(prefix string, count int, startOrdinal int64) []sourceledger.Effect {
	effects := make([]sourceledger.Effect, 0, count)
	for i := range count {
		path := fmt.Sprintf("%s%02d.go", prefix, i)
		effect := briefEffect("file-"+path, path, startOrdinal-int64(i), api.SourceChangeOriginExternal, "s1")
		effect.CommandWindowID = "window-1"
		effects = append(effects, effect)
	}
	return effects
}

func TestACOC01OwnCommandRowsFollowForeignRowsWithinTheCap(t *testing.T) {
	foreign, own := briefFiles("person", 18, 100), ownCommandFiles("gen", 5, 1000)
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: map[string]bool{}, Effects: slices.Concat(own, foreign),
	})
	if len(brief.Files) != sourceChangeBriefFileCap || brief.OtherFiles != 3 || brief.OtherEffects != 3 {
		t.Fatalf("brief = %+v, want 18 foreign and 2 own rows with 3 counted", brief)
	}
	for i, row := range brief.Files {
		wantOwn := i >= len(foreign)
		if row.DuringYourCommand != wantOwn {
			t.Fatalf("row %d = %+v, want during_your_command %v", i, row, wantOwn)
		}
		if wantOwn && row.Actor != string(sourceledger.ActorExternal) {
			t.Fatalf("own row actor = %q, want the class unchanged", row.Actor)
		}
	}
}

func TestACOC01OwnRowsNeverDisplaceAPersonsEdit(t *testing.T) {
	foreign, own := briefFiles("person", sourceChangeBriefFileCap+1, 100), ownCommandFiles("gen", 4, 1000)
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: map[string]bool{}, Effects: slices.Concat(own, foreign),
	})
	if got := briefRowPaths(brief, false); !slices.Equal(got, effectPaths(foreign[:sourceChangeBriefFileCap])) {
		t.Fatalf("rows = %v, want only foreign rows", got)
	}
	if brief.OtherFiles != 5 {
		t.Fatalf("other files = %d, want the last foreign file and all four own files", brief.OtherFiles)
	}
}

func TestACOC01FileAnotherActorAlsoChangedIsThatActorsRow(t *testing.T) {
	own := ownCommandFiles("shared", 1, 100)[0]
	person := briefEffect(own.FileID, own.Path, 90, api.SourceChangeOriginUser, "")
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: map[string]bool{}, Effects: []sourceledger.Effect{own, person},
	})
	want := inject.SourceChangeFile{Path: own.Path, Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}
	if len(brief.Files) != 1 || brief.Files[0] != want || brief.OtherFiles != 0 {
		t.Fatalf("brief = %+v, want one row for the person's edit", brief)
	}
}

func TestACOC01AnotherSessionsWindowIsForeign(t *testing.T) {
	other := ownCommandFiles("gen", 1, 100)
	other[0].SessionID = "s2"
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: map[string]bool{}, Effects: other,
	})
	if len(brief.Files) != 1 || brief.Files[0].DuringYourCommand {
		t.Fatalf("brief = %+v, want an unflagged row", brief)
	}
}

func TestACOC01OwnRowsListTheWorkingSetFirst(t *testing.T) {
	outside, inside := ownCommandFiles("gen", 2, 1000), ownCommandFiles("read", 2, 500)
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: briefTouched(inside), Effects: slices.Concat(outside, inside),
	})
	if got := briefRowPaths(brief, true); !slices.Equal(got, effectPaths(inside)) || !brief.Files[0].WorkingSet {
		t.Fatalf("working-set rows = %v, want %v leading", got, effectPaths(inside))
	}
}

// A foreign last row proves every foreign working-set file was itemized but
// says nothing about the own command files counted behind it.
func TestACOC01CountedOwnFilesKeepTheElsewhereClaimTrue(t *testing.T) {
	foreign, own := briefFiles("person", sourceChangeBriefFileCap, 100), ownCommandFiles("gen", 3, 1000)
	cases := []struct {
		name    string
		touched map[string]bool
		want    bool
	}{
		{"counted own files were read", briefTouched(own[:1]), true},
		{"counted own files were not read", map[string]bool{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
				SessionID: "s1", Roots: briefRoots(), Touched: tc.touched, Effects: slices.Concat(own, foreign),
			})
			last := brief.Files[len(brief.Files)-1]
			if last.WorkingSet || last.DuringYourCommand || brief.OtherFiles != 3 {
				t.Fatalf("brief = %+v, want a foreign unflagged last row and 3 counted", brief)
			}
			if got := brief.OtherMayIncludeWorkingSet(); got != tc.want {
				t.Fatalf("OtherMayIncludeWorkingSet = %v, want %v", got, tc.want)
			}
		})
	}
}

// Rows reach the last class only after every foreign file fit, so an unflagged
// last own row proves every working-set file of every class was itemized.
func TestACOC01OwnLastRowDecidesTheElsewhereClaim(t *testing.T) {
	foreign, outside := briefFiles("person", 18, 100), ownCommandFiles("gen", 5, 1000)
	inside := ownCommandFiles("read", 2, 2000)
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: briefTouched(inside), Effects: slices.Concat(inside, outside, foreign),
	})
	last := brief.Files[len(brief.Files)-1]
	if !last.DuringYourCommand || !last.WorkingSet || !brief.OtherMayIncludeWorkingSet() {
		t.Fatalf("brief = %+v, want a flagged last row to keep the wider claim", brief)
	}
	brief = assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(), Touched: briefTouched(inside[:1]), Effects: slices.Concat(inside[:1], outside, foreign),
	})
	if last = brief.Files[len(brief.Files)-1]; !last.DuringYourCommand || last.WorkingSet || brief.OtherMayIncludeWorkingSet() {
		t.Fatalf("brief = %+v, want every working-set file itemized", brief)
	}
}
