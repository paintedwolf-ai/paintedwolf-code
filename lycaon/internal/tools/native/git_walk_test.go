package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type walkGitReader struct{ manager *git.Manager }

func (r walkGitReader) HeadState(_ context.Context, path string) gitstate.State {
	head, _ := git.ReadHeadSHA(path)
	branch, _ := git.ReadBranch(path)
	return gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: head, HeadRef: branch}
}

func (r walkGitReader) RefLogHead(ctx context.Context, path string, limit int) ([]gitstate.RefLogEntry, error) {
	rows, err := r.manager.RefLogHead(ctx, path, limit)
	out := make([]gitstate.RefLogEntry, len(rows))
	for i, row := range rows {
		out[i] = gitstate.RefLogEntry{Commit: row.Commit, Subject: row.Subject}
	}
	return out, err
}

type nativeWalkFixture struct {
	dir     string
	manager *git.Manager
	ledger  *sourceledger.Store
	tc      tools.ToolContext
}

func newNativeWalkFixture(t *testing.T) nativeWalkFixture {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	testdbseed.InsertSession(t, sqlDB, "s1", "p1")
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots
 (id, project_id, path, label, is_primary, added_at, kind)
 VALUES ('r1','p1',?,'Repository',1,?,'attached')`, dir, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "seed source root", err)
	manager := git.NewManager()
	ledger := sourceledger.New(sqlDB, t.TempDir())
	ledger.Git.SetGitReader(walkGitReader{manager})
	tc := nativefixture.Context(dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Source.ActiveRootID, tc.Identity.UserTurn = "p1", "s1", "r1", 1
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands = ledger.Commands
	tc.Source.Observations = ledger.Inventory
	tc.Source.GitMutations = ledger.Git
	return nativeWalkFixture{dir: dir, manager: manager, ledger: ledger, tc: tc}
}

func TestNativeCommitAndAmendProduceStandaloneWalkSteps(t *testing.T) {
	fixture := newNativeWalkFixture(t)
	dir, manager, ledger, tc := fixture.dir, fixture.manager, fixture.ledger, fixture.tc
	tc.Invocation.ToolName = "git_commit"
	tool := &GitCommitTool{Git: manager, Boundary: nativefixture.Boundary(t)}
	for i, name := range []string{"first.txt", "second.txt"} {
		testutil.FailErr(t, "write existing work", os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
		tc.Identity.ToolCallID = name
		out, err := tool.Run(t.Context(), map[string]any{"message": name, "paths": []any{name}, "amend": i == 1}, tc)
		testutil.FailErr(t, "commit existing work", err)
		var receipt struct {
			Hash      string `json:"hash"`
			Available bool   `json:"available"`
		}
		testutil.FailErr(t, "read commit receipt", json.Unmarshal([]byte(out), &receipt))
		if !receipt.Available || receipt.Hash == "" {
			t.Fatalf("missing commit receipt: %s", out)
		}
		page, err := ledger.Walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 1}, 10, 0, sourceledger.CommitLens{})
		testutil.FailErr(t, "read native commit walk", err)
		kind := "commit"
		if i == 1 {
			kind = "amend"
		}
		if len(page.Files) != 0 || len(page.GitChanges) != i+1 || page.GitChanges[0].Kind != kind || page.GitChanges[0].ToCommit != receipt.Hash || page.GitChanges[0].ToolCallID != name {
			t.Fatalf("native commit walk = %+v", page)
		}
	}
	_, _ = tool.Run(t.Context(), map[string]any{"message": "Nothing changed", "paths": []any{"first.txt"}}, tc)
	page, err := ledger.Walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read after failed commit", err)
	if len(page.GitChanges) != 2 {
		t.Fatalf("failed commit manufactured movement: %+v", page.GitChanges)
	}
}

func TestNativeCheckoutRecordsReviewedPathsWithItsGitMovement(t *testing.T) {
	f := newNativeWalkFixture(t)
	path := filepath.Join(f.dir, "file.txt")
	testutil.FailErr(t, "write main file", os.WriteFile(path, []byte("main"), 0o644))
	gittest.CommitAll(t, f.dir, "Initial")
	gittest.Run(t, f.dir, "branch", "-M", "main")
	gittest.Run(t, f.dir, "checkout", "-b", "feature")
	testutil.FailErr(t, "write feature file", os.WriteFile(path, []byte("feature"), 0o644))
	gittest.CommitAll(t, f.dir, "Feature")
	_, err := f.ledger.TrackFile(t.Context(), sourceledger.TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "file.txt", EntryKind: sourceledger.EntryKindFile,
		Content: []byte("feature"), SHA256: sourceblob.ContentSHA([]byte("feature")), Size: 7,
	})
	testutil.FailErr(t, "track current file", err)
	f.tc.Identity.ToolCallID, f.tc.Invocation.ToolName = "checkout-call", "git_checkout"
	f.tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	tool := GitOperationTool{Git: f.manager, Boundary: nativefixture.Boundary(t), Kind: "checkout"}
	_, err = tool.Run(t.Context(), map[string]any{"branch": "main"}, f.tc)
	testutil.FailErr(t, "checkout main", err)
	page, err := f.ledger.Walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 1}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read checkout walk", err)
	if len(page.Files) != 1 || len(page.GitChanges) != 1 || page.GitChanges[0].Kind != "checkout" {
		t.Fatalf("missing checkout history: %+v", page)
	}
	effect := page.Files[0].Effects[0]
	if effect.ToolCallID != "checkout-call" || effect.GitTransitionID != page.GitChanges[0].ID {
		t.Fatalf("unattributed checkout file: %+v", effect)
	}
	_, err = f.ledger.Inventory.ObservePaths(t.Context(), "p1", []sourceledger.RootSpec{{ID: "r1", Path: f.dir}}, []sourceledger.PathRef{{RootID: "r1", Path: "file.txt"}})
	testutil.FailErr(t, "observe checkout again", err)
	page, err = f.ledger.Walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read stable checkout history", err)
	if len(page.Files[0].Effects) != 1 || len(page.GitChanges) != 1 {
		t.Fatalf("duplicated checkout: %+v", page)
	}
}
