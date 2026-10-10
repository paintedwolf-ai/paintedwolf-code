package sourceledger

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A file with a head on trunk and on a chat's worktree answers with the head
// of the branch the request reads, whichever row the database scans last.
func TestWalkTipsReadTheRequestedBranch(t *testing.T) {
	store, ctx := openLedger(t)
	const worktree = sourcebranch.ID("worktree:w1")
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "trunk-write", After: []byte("trunk\n")})
	fileID, trunkVersion := mustResolve(t, store, ctx, "a.go")
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.go"},
		ProjectID:      "p1", BranchID: worktree, FileID: fileID,
		DerivedFromVersionID: trunkVersion,
		Op:                   api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "worktree-write", After: []byte("tree\n")})
	_, treeVersion, err := store.History.ResolveFile(ctx, "p1", worktree, "r1", "a.go")
	testutil.FailErr(t, "resolve worktree head", err)
	if treeVersion == trunkVersion {
		t.Fatal("fixture: the worktree write did not advance its own head")
	}

	for _, tc := range []struct {
		name   string
		branch sourcebranch.ID
		want   string
	}{
		{name: "trunk", branch: sourcebranch.Trunk, want: "trunk\n"},
		{name: "worktree", branch: worktree, want: "tree\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{
				Kind: BaselineTurn, SessionID: "s1", Turn: 1,
				RootBranches: map[string]sourcebranch.ID{"r1": tc.branch},
			}, 10, 0, CommitLens{})
			testutil.FailErr(t, "query walk", err)
			if len(walk.Files) != 1 {
				t.Fatalf("files = %+v, want a.go once", walk.Files)
			}
			file := walk.Files[0]
			if file.FileID != fileID {
				t.Fatalf("file id = %q, want %q", file.FileID, fileID)
			}
			if sum := sha256.Sum256([]byte(tc.want)); file.Tip.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("tip = %+v, want the %s head", file.Tip, tc.name)
			}
			for _, effect := range file.Effects {
				if effect.BranchID != tc.branch {
					t.Fatalf("effect on %q listed under %q", effect.BranchID, tc.branch)
				}
			}
		})
	}
}

// Turn zero stamps edits made while no turn ran, so no chat has a turn zero.
func TestWalkTurnZeroListsNothing(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "idle.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		SessionID: "s1", OperationID: "idle-edit", After: []byte("idle\n")})
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineTurn, SessionID: "s1"}, 10, 0, CommitLens{Available: true})
	testutil.FailErr(t, "query walk", err)
	if len(walk.Files) != 0 || walk.NextBeforeOrdinal != 0 {
		t.Fatalf("turn zero listed %+v", walk.Files)
	}
	if got := walk.Baseline.String(); got != "turn:s1,0" {
		t.Fatalf("baseline = %q, want turn:s1,0", got)
	}
	if !walk.CommitAvailable {
		t.Fatal("an empty turn still reports whether Git can answer")
	}
}

func TestCurrentTurnBaselineGrammar(t *testing.T) {
	for raw, want := range map[string]bool{
		"turn:s1":   true,
		" turn:s1 ": true,
		"turn:s1,2": false,
		"turn:":     false,
		"turn: ":    false,
		"session:1": false,
	} {
		bas, ok := ParseCurrentTurn(raw)
		if ok != want {
			t.Fatalf("ParseCurrentTurn(%q) = %v, want %v", raw, ok, want)
		}
		if ok && (bas.Kind != BaselineTurn || bas.SessionID != "s1" || bas.Turn != 0) {
			t.Fatalf("ParseCurrentTurn(%q) = %+v", raw, bas)
		}
	}
	for _, raw := range []string{"presentation", "commit", "session:s1", "pin:p1", "turn:s1,3"} {
		bas, err := ParseBaseline(raw)
		testutil.FailErr(t, "parse "+raw, err)
		if got := bas.String(); got != raw {
			t.Fatalf("String(ParseBaseline(%q)) = %q", raw, got)
		}
	}
}
