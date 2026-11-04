package sourcebrief

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
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
