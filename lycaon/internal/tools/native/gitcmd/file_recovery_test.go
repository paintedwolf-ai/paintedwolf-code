package gitcmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// git rm and git mv route worktree changes through recoverable source owners.
func TestTrackedRemovalAndMoveSourceRecovery(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"rm", "rm-undo", "mv"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			h := newApplyHarness(t)
			database := testdbfixture.Open(t, "store.db")
			rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, h.root)
			ledger := sourceledger.New(database, filepath.Join(t.TempDir(), "content"))
			service := projectsource.NewSourceMutationService(database, ledger)
			t.Cleanup(func() { testutil.FailErr(t, "close source service", service.Close()) })
			h.tc.Identity.ProjectID = testdbseed.DefaultProjectID
			h.tc.Identity.UserTurn = 1
			h.tc.Source.Roots[0].ID = rootID
			h.tc.Source.ActiveRootID = rootID
			h.tc.Source.SourceLedger = ledger
			h.tc.Source.SourceMutations = service
			h.tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
			h.tc.Source.GitMutations = ledger.Git
			h.tc.Source.ProjectSourceBranch = sourcebranch.Trunk
			var err error
			h.paths, err = projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, "")
			testutil.FailErr(t, "paths", err)
			p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: h.root, IsPrimary: true}}}
			target := "sub/b.txt"
			if command == "rm" {
				// restore_version returns to a version the ledger retains.
				target = "tracked.go"
				_, err := (&native.WriteTool{Boundary: h.owners.Boundary}).Run(t.Context(), map[string]any{"path": target, "content": "package tracked\n"}, h.tc)
				testutil.FailErr(t, "versioned write", err)
				h.git("add", "--", target)
				h.git("commit", "-q", "-m", "tracked")
			}
			args := []string{"git", strings.TrimSuffix(command, "-undo"), "--", target}
			if command == "mv" {
				args = append(args, "renamed.txt")
			}
			got := h.exec(args...)
			testutil.FailErr(t, "tracked mutation", got.err)
			if got.result.Status != 0 {
				t.Fatalf("mutation %+v %s", got.result, got.stderr)
			}
			switch command {
			case "rm":
				_, err := (&native.RestoreVersionTool{Boundary: h.owners.Boundary}).Run(t.Context(), map[string]any{"path": target}, h.tc)
				testutil.FailErr(t, "restore_version after git rm", err)
				if h.read(target) != "package tracked\n" {
					t.Fatal("restore_version recovered different bytes")
				}
			case "rm-undo":
				// The emptied parent comes back first, then the file inside it.
				for _, kind := range []string{"delete", "delete"} {
					state, err := service.History.State(t.Context(), p.ID)
					testutil.FailErr(t, "source history", err)
					if state.Undo == nil || state.Undo.Kind != kind {
						t.Fatalf("undo head %+v", state.Undo)
					}
					_, err = service.Undo(t.Context(), uuid.NewString(), p, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: state.Undo.ID})
					testutil.FailErr(t, "undo", err)
				}
				if h.read("sub/b.txt") != "bee\n" {
					t.Fatal("undo recovered different bytes")
				}
			case "mv":
				walk, err := ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: h.tc.Identity.SessionID}, 10, 0, sourceledger.CommitLens{})
				testutil.FailErr(t, "ledger walk", err)
				found := false
				for _, file := range walk.Files {
					for _, effect := range file.Effects {
						found = found || (file.Path == "renamed.txt" && effect.Op == wire.SourceChangeOpRename && effect.FromPath == "sub/b.txt")
					}
				}
				if !found {
					t.Fatalf("ledger lacks the rename: %+v", walk.Files)
				}
				return
			}
			if staged := h.git("ls-files", "--", target); staged != "" {
				t.Fatal("source recovery changed the index")
			}
		})
	}
}
