package session

import (
	"testing"
	"time"

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

func TestAssembleSourceChangeBriefPartitionsWorkingSet(t *testing.T) {
	brief := assembleSourceChangeBrief(sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{"a.go": true},
		Effects: []sourceledger.Effect{
			// Newest first: two user effects on a touched file, one agent effect
			// on an untouched file, and one of the session's own effects.
			briefEffect("f1", "a.go", 9, api.SourceChangeOriginUser, ""),
			briefEffect("f2", "b.go", 8, api.SourceChangeOriginAgent, "other-session"),
			briefEffect("f1", "a.go", 7, api.SourceChangeOriginUser, ""),
			briefEffect("f3", "c.go", 6, api.SourceChangeOriginAgent, "s1"),
		},
	})
	if len(brief.Files) != 1 {
		t.Fatalf("itemized files = %d, want just the touched one", len(brief.Files))
	}
	row := brief.Files[0]
	if row.Path != "a.go" || row.Actor != "user" || row.Effects != 2 || row.At != "14:02 UTC" {
		t.Fatalf("itemized row = %+v, want a.go user x2 at 14:02 UTC", row)
	}
	if brief.OtherFiles != 1 || brief.OtherEffects != 1 {
		t.Fatalf("elsewhere = %d files / %d effects, want 1/1 — own effects never count", brief.OtherFiles, brief.OtherEffects)
	}
}

func TestAssembleSourceChangeBriefScopesToTrunk(t *testing.T) {
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

func TestAssembleSourceChangeBriefCapsItemizedRows(t *testing.T) {
	in := sourceChangeBriefInputs{
		SessionID: "s1", Roots: briefRoots(),
		Touched: map[string]bool{},
	}
	for i := 0; i < sourceChangeBriefFileCap+5; i++ {
		path := "f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go"
		in.Touched[path] = true
		in.Effects = append(in.Effects, briefEffect("file-"+path, path, int64(1000-i), api.SourceChangeOriginUser, ""))
	}
	brief := assembleSourceChangeBrief(in)
	if len(brief.Files) != sourceChangeBriefFileCap || brief.OtherFiles != 5 {
		t.Fatalf("cap = %d itemized / %d elsewhere, want %d/5", len(brief.Files), brief.OtherFiles, sourceChangeBriefFileCap)
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
