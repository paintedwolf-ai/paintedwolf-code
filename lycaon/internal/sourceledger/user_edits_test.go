package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompareScopeUnmarksUserEdits(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "notes.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "person-creates", After: []byte("a\nb\nc\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "notes.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		OperationID: "agent-edits", After: []byte("a\nB\nc\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "notes.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
		OperationID: "person-edits", After: []byte("A\nB\nc\n"),
	})
	fileID, _ := mustResolve(t, store, ctx, "notes.txt")

	marked, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{}, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare with user edits marked", err)
	if marked.UserEditsUnmarked || marked.Before.State != "absent" {
		t.Fatalf("marked comparison = %+v, want the absent pre-image", marked.Before)
	}

	unmarked, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{}, fileID,
		ScopeComparisonOptions{UnmarkUserEdits: true})
	testutil.FailErr(t, "compare with user edits unmarked", err)
	if !unmarked.UserEditsUnmarked || unmarked.Attribution == nil {
		t.Fatal("comparison did not project author marks")
	}
	if unmarked.Before != marked.Before || unmarked.After != marked.After {
		t.Fatal("marking changed saved endpoints")
	}
	visible := ""
	for _, run := range unmarked.Attribution.After {
		if run.Visible {
			visible += unmarked.After.Content[run.Index : run.Index+run.Length]
		}
	}
	if visible != "B" {
		t.Fatalf("marked text %q, want only the agent's inserted character", visible)
	}

}

func TestWalkWithoutUserEditsDropsFilesOnlyThePersonChanged(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "mine.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "person-writes", After: []byte("mine\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "theirs.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		OperationID: "agent-writes", After: []byte("theirs\n"),
	})
	paths := func(baseline Baseline) []string {
		t.Helper()
		res, err := store.Walk.QueryWalk(ctx, "p1", baseline, 100, 0, CommitLens{})
		testutil.FailErr(t, "query walk", err)
		out := make([]string, 0, len(res.Files))
		for _, file := range res.Files {
			for _, effect := range file.Effects {
				if baseline.WithoutUserEdits && effect.Origin == api.SourceChangeOriginUser {
					t.Fatalf("walk kept a user effect on %s", file.Path)
				}
			}
			out = append(out, file.Path)
		}
		return out
	}
	if got := paths(Baseline{Kind: BaselinePresentation}); len(got) != 2 {
		t.Fatalf("walk with user edits = %v, want both files", got)
	}
	got := paths(Baseline{Kind: BaselinePresentation, WithoutUserEdits: true})
	if len(got) != 1 || got[0] != "theirs.txt" {
		t.Fatalf("walk without user edits = %v, want only theirs.txt", got)
	}
}

func TestRecordedComparisonSelectsChatAndPreservesOtherAuthors(t *testing.T) {
	store, ctx := openLedger(t)
	previous := ""
	for _, step := range []struct {
		chat, text string
		origin     api.SourceChangeOrigin
	}{
		{"a", "first\n", api.SourceChangeOriginAgent},
		{"person", "first typed\n", api.SourceChangeOriginUser},
		{"b", "first typed\nsecond\n", api.SourceChangeOriginAgent},
	} {
		mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "notes.txt", Op: api.SourceChangeOpWrite, Origin: step.origin,
			SessionID: step.chat, Turn: 1, OperationID: step.chat, Before: []byte(previous), After: []byte(step.text)})
		previous = step.text
	}
	fileID, _ := mustResolve(t, store, ctx, "notes.txt")
	for _, chat := range []string{"a", "b"} {
		comparison, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{Kind: BaselineSession, SessionID: chat}, fileID, ScopeComparisonOptions{UnmarkUserEdits: true})
		testutil.FailErr(t, "compare recorded chat", err)
		if comparison.Attribution == nil || comparison.After.Content != previous {
			t.Fatal("recorded comparison lost endpoints or authorship")
		}
		selected, hidden := "", ""
		for _, run := range comparison.Attribution.After {
			value := comparison.After.Content[run.Index : run.Index+run.Length]
			if run.Selected {
				selected += value
			}
			if !run.Visible {
				hidden += value
			}
		}
		want := "first\n"
		if chat == "b" {
			want = "second\n"
		}
		if selected != want {
			t.Fatalf("chat %s marked %q, want %q", chat, selected, want)
		}
		if chat == "a" && hidden != " typed" {
			t.Fatalf("human insertion hidden=%q", hidden)
		}
	}
}
