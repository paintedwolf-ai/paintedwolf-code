package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWalkScopesCheckoutsBeforePagination(t *testing.T) {
	store, ctx := openLedger(t)
	checkout := sourcebranch.ForWorktree("checkout")
	for _, branch := range []sourcebranch.ID{sourcebranch.Trunk, checkout} {
		mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "a.txt", BranchID: branch,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, SessionID: "chat", Turn: 1,
			OperationID: "write-" + branch.String(), After: []byte("from " + branch.String())})
	}
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/base":     {Repo: gitstate.RepoPresent, HeadCommit: "before"},
		"/checkout": {Repo: gitstate.RepoPresent, HeadCommit: "before"},
	}}
	store.Git.SetGitReader(reader)
	for _, root := range []RootSpec{{ID: "r1", Path: "/base"}, {ID: "r1", Path: "/checkout", BranchID: checkout}} {
		_, err := store.Git.ObserveGitState(ctx, "p1", []RootSpec{root})
		testutil.FailErr(t, "seed checkout", err)
		reader.states[root.Path] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: root.Path}
		_, err = store.Git.ObserveGitState(ctx, "p1", []RootSpec{root})
		testutil.FailErr(t, "move checkout head", err)
	}
	for _, kind := range []BaselineKind{BaselinePresentation, BaselineSession, BaselineTurn} {
		for _, branch := range []sourcebranch.ID{sourcebranch.Trunk, checkout} {
			baseline := Baseline{Kind: kind, SessionID: "chat", Turn: 1,
				RootBranches: map[string]sourcebranch.ID{"r1": branch}}
			var files []WalkFile
			var movements []GitTransition
			var before int64
			for {
				walk, err := store.Walk.QueryWalk(ctx, "p1", baseline, 1, before, CommitLens{})
				testutil.FailErr(t, "query checkout walk", err)
				files = append(files, walk.Files...)
				movements = append(movements, walk.GitChanges...)
				if walk.NextBeforeOrdinal == 0 {
					break
				}
				if before != 0 && walk.NextBeforeOrdinal >= before {
					t.Fatal("cursor did not advance")
				}
				before = walk.NextBeforeOrdinal
			}
			if len(files) != 1 || len(files[0].Effects) != 1 || files[0].Effects[0].BranchID != branch {
				t.Fatalf("%s/%q crossed checkout: %+v", kind, branch, files)
			}
			if kind != BaselinePresentation {
				if len(movements) != 0 {
					t.Fatalf("unaffiliated Git movements entered a session: %+v", movements)
				}
				continue
			}
			wantCommit := "/base"
			if branch == checkout {
				wantCommit = "/checkout"
			}
			if len(movements) != 1 || movements[0].ToCommit != wantCommit {
				t.Fatalf("%s/%q crossed Git history: %+v", kind, branch, movements)
			}
		}
	}
}
