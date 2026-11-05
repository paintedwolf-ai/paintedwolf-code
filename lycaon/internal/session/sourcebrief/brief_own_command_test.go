package sourcebrief

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

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
